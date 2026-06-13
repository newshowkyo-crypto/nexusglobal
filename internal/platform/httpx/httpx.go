package httpx

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
)

type contextKey string

const (
	ContextKeyMerchantID contextKey = "merchant_id"
	ContextKeyAdminUser  contextKey = "admin_user"
	ContextKeySubject    contextKey = "subject"
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

func AdminAuth(jwtSecret string) func(http.Handler) http.Handler {
	if jwtSecret == "" {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeErrorJSON(w, http.StatusInternalServerError, "INTERNAL", "JWT_SECRET not configured", r.Header.Get("X-Request-ID"))
			})
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("X-Request-ID")
			tokenStr := extractBearerToken(r)
			if tokenStr == "" {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid Authorization header", reqID)
				return
			}

			token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
				}
				return []byte(jwtSecret), nil
			})
			if err != nil || !token.Valid {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired token", reqID)
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token claims", reqID)
				return
			}

			subject, _ := claims["sub"].(string)
			if subject == "" {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing subject in token", reqID)
				return
			}

			ctx := context.WithValue(r.Context(), ContextKeySubject, subject)
			if username, ok := claims["username"].(string); ok {
				ctx = context.WithValue(ctx, ContextKeyAdminUser, username)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func MerchantAuth(jwtSecret string) func(http.Handler) http.Handler {
	if jwtSecret == "" {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeErrorJSON(w, http.StatusInternalServerError, "INTERNAL", "JWT_SECRET not configured", r.Header.Get("X-Request-ID"))
			})
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("X-Request-ID")
			tokenStr := extractBearerToken(r)
			if tokenStr == "" {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid Authorization header", reqID)
				return
			}

			token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
				}
				return []byte(jwtSecret), nil
			})
			if err != nil || !token.Valid {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired token", reqID)
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token claims", reqID)
				return
			}

			subject, _ := claims["sub"].(string)
			if subject == "" {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing subject in token", reqID)
				return
			}

			merchantID, ok := claims["merchant_id"].(string)
			if !ok || merchantID == "" {
				writeErrorJSON(w, http.StatusForbidden, "FORBIDDEN", "token missing merchant_id claim", reqID)
				return
			}

			ctx := context.WithValue(r.Context(), ContextKeyMerchantID, merchantID)
			ctx = context.WithValue(ctx, ContextKeySubject, subject)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func InternalAuth(sharedSecret string) func(http.Handler) http.Handler {
	if sharedSecret == "" {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeErrorJSON(w, http.StatusInternalServerError, "INTERNAL", "INTERNAL_TOKEN not configured", r.Header.Get("X-Request-ID"))
			})
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("X-Request-ID")
			token := r.Header.Get("X-Internal-Token")
			if token == "" {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing X-Internal-Token header", reqID)
				return
			}
			if subtle.ConstantTimeCompare([]byte(token), []byte(sharedSecret)) != 1 {
				writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid internal token", reqID)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

func GetMerchantID(ctx context.Context) string {
	if v, ok := ctx.Value(ContextKeyMerchantID).(string); ok {
		return v
	}
	return ""
}

func GetSubject(ctx context.Context) string {
	if v, ok := ctx.Value(ContextKeySubject).(string); ok {
		return v
	}
	return ""
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
