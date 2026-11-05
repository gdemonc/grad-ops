// Package api exposes the REST/JSON interface.
//
// Handlers validate input and translate between wire DTOs and domain types.
// They never execute host operations: the API is not an execution surface.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/store"
	"github.com/gdemonc/grad-ops/internal/task"
)

// Deps are the collaborators a router needs.
type Deps struct {
	Logger          *slog.Logger
	Tasks           *task.Service
	DB              *store.DB
	Version         string
	MaxRequestBytes int64
}

// New builds the HTTP handler with all routes and middleware.
func New(d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.MaxRequestBytes <= 0 {
		d.MaxRequestBytes = 1 << 20
	}

	h := &handlers{deps: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /api/health", h.health)
	mux.HandleFunc("POST /api/tasks", h.createTask)
	mux.HandleFunc("GET /api/tasks", h.listTasks)
	mux.HandleFunc("GET /api/tasks/{id}", h.getTask)
	mux.HandleFunc("GET /api/tasks/{id}/timeline", h.timeline)
	mux.HandleFunc("GET /api/audit/verify", h.verifyAudit)

	return recoveryMiddleware(d.Logger, loggingMiddleware(d.Logger, mux))
}

type handlers struct {
	deps Deps
}

// --- responses -------------------------------------------------------------

type taskDTO struct {
	ID           int64   `json:"id"`
	RequestText  string  `json:"request_text"`
	Source       string  `json:"source"`
	Status       string  `json:"status"`
	Summary      *string `json:"summary"`
	FailureStage *string `json:"failure_stage"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

type createTaskRequest struct {
	RequestText string `json:"request_text"`
	Source      string `json:"source"`
}

type timelineEventDTO struct {
	Seq       int64           `json:"seq"`
	TaskID    *int64          `json:"task_id"`
	Type      string          `json:"type"`
	Actor     string          `json:"actor"`
	Message   string          `json:"message"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

type auditVerifyDTO struct {
	OK        bool   `json:"ok"`
	Events    int    `json:"events"`
	BrokenSeq int64  `json:"broken_seq,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type errorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	Error errorDTO `json:"error"`
}

func toTaskDTO(t domain.Task) taskDTO {
	return taskDTO{
		ID:           t.ID,
		RequestText:  t.RequestText,
		Source:       t.Source,
		Status:       string(t.Status),
		Summary:      t.Summary,
		FailureStage: t.FailureStage,
		CreatedAt:    domain.FormatTime(t.CreatedAt),
		UpdatedAt:    domain.FormatTime(t.UpdatedAt),
	}
}

// --- handlers --------------------------------------------------------------

func (h *handlers) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": "grad-ops",
		"version": h.deps.Version,
	})
}

func (h *handlers) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 2*time.Second)
	defer cancel()

	if err := h.deps.DB.PingContext(ctx); err != nil {
		h.deps.Logger.Error("database health check failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "database unavailable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"service":  "grad-ops",
		"version":  h.deps.Version,
		"database": "ok",
	})
}

func (h *handlers) createTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if err := h.decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	created, err := h.deps.Tasks.Create(r.Context(), task.CreateInput{
		RequestText: req.RequestText,
		Source:      req.Source,
	})
	if err != nil {
		h.writeServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, toTaskDTO(created))
}

func (h *handlers) listTasks(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	var status *domain.Status
	if raw := strings.TrimSpace(query.Get("status")); raw != "" {
		parsed, err := domain.ParseStatus(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		status = &parsed
	}

	limit := 0
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be a positive integer")
			return
		}
		limit = parsed
	}

	tasks, err := h.deps.Tasks.List(r.Context(), status, limit)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}

	out := make([]taskDTO, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, toTaskDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) getTask(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	found, err := h.deps.Tasks.Get(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(found))
}

func (h *handlers) timeline(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	events, err := h.deps.Tasks.Timeline(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}

	out := make([]timelineEventDTO, 0, len(events))
	for _, ev := range events {
		out = append(out, timelineEventDTO{
			Seq:       ev.Seq,
			TaskID:    ev.TaskID,
			Type:      ev.Type,
			Actor:     ev.Actor,
			Message:   ev.Message,
			Payload:   ev.Payload,
			CreatedAt: domain.FormatTime(ev.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// verifyAudit reports whether the stored chain is internally consistent.
func (h *handlers) verifyAudit(w http.ResponseWriter, r *http.Request) {
	result, err := verifyChain(r, h.deps.DB)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}

	status := http.StatusOK
	if !result.OK {
		// A broken chain is a server-side integrity failure: answer 500 so a
		// monitoring check cannot mistake it for a healthy response.
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, auditVerifyDTO{
		OK:        result.OK,
		Events:    result.Events,
		BrokenSeq: result.BrokenSeq,
		Reason:    result.Reason,
	})
}

// --- helpers ---------------------------------------------------------------

func (h *handlers) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, h.deps.MaxRequestBytes)

	decoder := json.NewDecoder(r.Body)
	// Unknown fields are rejected: a client that misspells a field must learn
	// about it instead of silently getting a different action.
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON body: unexpected trailing data")
	}
	return nil
}

func (h *handlers) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidRequest), errors.Is(err, domain.ErrInvalidTransition):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		h.deps.Logger.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

func parseID(r *http.Request) (int64, error) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%w: task id must be a positive integer", domain.ErrInvalidRequest)
	}
	return id, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: errorDTO{Code: code, Message: message}})
}
