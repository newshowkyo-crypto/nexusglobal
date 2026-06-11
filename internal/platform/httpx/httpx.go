package httpx

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/google/uuid"
)

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", id)
		r.Header.Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if p := recover(); p != nil {
					logger.Error("panic recovered",
						"panic", p,
						"stack", string(debug.Stack()),
						"method", r.Method,
						"path", r.URL.Path)
					reqID := r.Header.Get("X-Request-ID")
					writeErrorJSON(w, http.StatusInternalServerError, "INTERNAL", "internal server error", reqID)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func AdminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid Authorization header", r.Header.Get("X-Request-ID"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func MerchantAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid Authorization header", r.Header.Get("X-Request-ID"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func InternalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Internal-Token")
		if token == "" {
			writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing X-Internal-Token header", r.Header.Get("X-Request-ID"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func HealthCheck(dbPing func() error, redisPing func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}
}

func ReadinessCheck(dbPing func() error, redisPing func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var failures []string
		if err := dbPing(); err != nil {
			failures = append(failures, "database: "+err.Error())
		}
		if err := redisPing(); err != nil {
			failures = append(failures, "redis: "+err.Error())
		}
		if len(failures) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			msg := strings.Join(failures, "; ")
			reqID := r.Header.Get("X-Request-ID")
			w.Write([]byte(`{"error":{"code":"NOT_READY","message":"` + msg + `","request_id":"` + reqID + `"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ready"}`))
	}
}

func writeErrorJSON(w http.ResponseWriter, status int, code, message, requestID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(`{"error":{"code":"` + code + `","message":"` + message + `","request_id":"` + requestID + `"}}`))
}
