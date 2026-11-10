package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/store"
)

// verifyChain checks the stored audit chain.
func verifyChain(r *http.Request, db *store.DB) (audit.Result, error) {
	return audit.Verify(r.Context(), db)
}

// contextWithTimeout bounds a handler's database work.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// bearerAuth requires a valid bearer token for every API route. Health checks
// stay open so a supervisor can probe the process without a credential.
//
// The comparison hashes both sides first, so it does not reveal the token's
// length through timing.
func bearerAuth(token string, next http.Handler) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		provided, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeMissingAuth(w)
			return
		}
		actual := sha256.Sum256([]byte(provided))
		if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			writeMissingAuth(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(header string) (string, bool) {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return "", false
	}
	if fields[1] == "" {
		return "", false
	}
	return fields[1], true
}

func writeMissingAuth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="gradops"`)
	writeError(w, http.StatusUnauthorized, "unauthorized", "a valid API bearer token is required")
}

// statusRecorder captures the status code for request logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

// loggingMiddleware records one line per request. Bodies and headers are never
// logged: request text and credentials must not land in the log.
func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// recoveryMiddleware turns a panic into a 500 instead of dropping the
// connection, and logs it without the request body.
func recoveryMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("panic serving request", "panic", recovered, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
