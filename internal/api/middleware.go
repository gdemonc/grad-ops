package api

import (
	"context"
	"log/slog"
	"net/http"
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
