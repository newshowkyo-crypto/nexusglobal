package httpx

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminAuth_ValidToken(t *testing.T) {
	secret := "test-secret"
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 收紧后应通过 AdminIdentity 获取身份
		identity, ok := GetAdminIdentity(r.Context())
		if !ok {
			t.Error("AdminIdentity not found in context")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte("subject:" + identity.Subject + ",role:" + identity.Role))
	}))

	// 收紧后 token 必须含 iss/aud/role/tenant_id（ADM-001：tenant 必填）
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":       "admin-123",
		"username":  "admin",
		"iss":       AdminIssuerExpected,
		"aud":       AdminAudienceExpected,
		"role":      AdminRoleSecurityAdmin,
		"tenant_id": "11111111-1111-1111-1111-111111111111",
		"exp":       time.Now().Add(time.Hour).Unix(),
	})
	tokenStr, err := token.SignedString([]byte(secret))
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "subject:admin-123,role:security-admin", rr.Body.String())
}

func TestAdminAuth_MissingToken(t *testing.T) {
	handler := AdminAuth("secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAdminAuth_InvalidToken(t *testing.T) {
	handler := AdminAuth("secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAdminAuth_ExpiredToken(t *testing.T) {
	secret := "test-secret"
	handler := AdminAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "admin-123",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	tokenStr, err := token.SignedString([]byte(secret))
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAdminAuth_EmptySecret(t *testing.T) {
	handler := AdminAuth("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestMerchantAuth_ValidToken(t *testing.T) {
	secret := "test-secret"
	handler := MerchantAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		merchantID := GetMerchantID(r.Context())
		subject := GetSubject(r.Context())
		w.Write([]byte("merchant:" + merchantID + ",subject:" + subject))
	}))

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":         "merchant-123",
		"merchant_id": "merch-456",
		"exp":         time.Now().Add(time.Hour).Unix(),
	})
	tokenStr, err := token.SignedString([]byte(secret))
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "merchant:merch-456,subject:merchant-123", rr.Body.String())
}

func TestMerchantAuth_MissingMerchantID(t *testing.T) {
	secret := "test-secret"
	handler := MerchantAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "merchant-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tokenStr, err := token.SignedString([]byte(secret))
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestInternalAuth_ValidToken(t *testing.T) {
	secret := "internal-secret"
	handler := InternalAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Internal-Token", secret)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestInternalAuth_InvalidToken(t *testing.T) {
	handler := InternalAuth("secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Internal-Token", "wrong-secret")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestInternalAuth_MissingToken(t *testing.T) {
	handler := InternalAuth("secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestInternalAuth_EmptySecret(t *testing.T) {
	handler := InternalAuth("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestRequestID_Generated(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Header.Get("X-Request-ID")))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.NotEmpty(t, rr.Header().Get("X-Request-ID"))
}

func TestRequestID_Preserved(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Header.Get("X-Request-ID")))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Request-ID", "custom-id")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, "custom-id", rr.Header().Get("X-Request-ID"))
	assert.Equal(t, "custom-id", rr.Body.String())
}

func TestHealthCheck(t *testing.T) {
	handler := HealthCheck(nil, nil)

	req := httptest.NewRequest("GET", "/healthz", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "ok")
}

func TestReadinessCheck_Success(t *testing.T) {
	handler := ReadinessCheck(func() error { return nil }, func() error { return nil })

	req := httptest.NewRequest("GET", "/readyz", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "ready")
}

func TestReadinessCheck_DBFailure(t *testing.T) {
	handler := ReadinessCheck(func() error { return assert.AnError }, func() error { return nil })

	req := httptest.NewRequest("GET", "/readyz", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestReadinessCheck_RedisFailure(t *testing.T) {
	handler := ReadinessCheck(func() error { return nil }, func() error { return assert.AnError })

	req := httptest.NewRequest("GET", "/readyz", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestRecover_Panic(t *testing.T) {
	logger := slog.Default()
	handler := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}
