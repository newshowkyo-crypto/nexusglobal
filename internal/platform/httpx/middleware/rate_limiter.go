package middleware

import (
	"net/http"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/ratelimit"
)

func RateLimit(limiter ratelimit.RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mc, ok := GetMerchantContext(r.Context())
			if !ok {
				WriteAPIError(w, apperr.New("MERCHANT_AUTH_REQUIRED", http.StatusUnauthorized))
				return
			}

			key := "merchant:" + mc.MerchantID.String()
			if mc.TenantID != "" {
				key = "tenant:" + mc.TenantID + ":merchant:" + mc.MerchantID.String()
			}

			allowed, err := limiter.Allow(r.Context(), key, mc.ProviderTier)
			if err != nil {
				if err == ratelimit.ErrFailClosed {
					WriteAPIError(w, apperr.New("RATE_LIMITER_FAIL_CLOSED", http.StatusServiceUnavailable))
				} else {
					WriteAPIError(w, apperr.ErrInternal)
				}
				return
			}

			if !allowed {
				WriteAPIError(w, apperr.New("MERCHANT_RATE_LIMITED", http.StatusTooManyRequests))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
