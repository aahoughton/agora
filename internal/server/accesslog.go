package server

import (
	"log/slog"
	"net/http"
	"time"
)

// logRequests logs one "http" line per request when it finishes: at debug
// for successes, info for client errors and warn for server errors. Streams
// are logged when they end, with their full duration. Query strings are
// left out; they can carry names but nothing the line needs.
func logRequests(l *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		level := slog.LevelDebug
		switch {
		case sw.status >= 500:
			level = slog.LevelWarn
		case sw.status >= 400:
			level = slog.LevelInfo
		}
		l.LogAttrs(r.Context(), level, "http",
			slog.String("method", r.Method), slog.String("path", r.URL.Path),
			slog.Int("status", sw.status), slog.Int64("bytes", sw.bytes),
			slog.Duration("dur", time.Since(start).Round(time.Millisecond)))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush keeps streaming responses working through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
