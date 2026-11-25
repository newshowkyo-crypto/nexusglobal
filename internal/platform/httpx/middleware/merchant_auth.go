package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type MerchantProfile struct {
	ID            uuid.UUID
	EntityID      uuid.UUID
	RiskProfileID uuid.UUID
	Status        string
	ProviderTier  string
	TenantID      string
}

// MerchantContext 是从已验证的认证结果 + 数据库 profile 构造的商户上下文。
//
// G13 修复：
//   - 所有字段来自数据库与认证结果，不接受请求体声明
//   - AllowedChannels 来自数据库授权能力，不硬编码
//   - 未知商户/空 profile/并发删除稳定返回 401/403，不 nil panic
type MerchantContext struct {
	MerchantID      uuid.UUID
	EntityID        uuid.UUID
	RiskProfileID   uuid.UUID
	Status          string
	ProviderTier    string
	TenantID        string
	AllowedChannels []string
	AuthenticatedAt time.Time
}

// MerchantVerifier 接口定义商户验证能力。
type MerchantVerifier interface {
	VerifyMerchant(ctx context.Context, merchantID uuid.UUID, signToken string) (bool, error)
	IsMerchantActive(ctx context.Context, merchantID uuid.UUID) (bool, error)
	GetMerchantProfile(ctx context.Context, merchantID uuid.UUID) (*MerchantProfile, error)
	// GetAllowedChannels 是 G13 新增：从数据库获取授权通道。
	// 实现可返回 nil（向后兼容），中间件会降级为空切片。
	GetAllowedChannels(ctx context.Context, merchantID uuid.UUID) ([]string, error)
}

type merchantAuthKey struct{}

// MerchantAuth 中间件校验商户身份。
//
// G13 修复：
//  1. verifier == nil 时返回 500（配置错误），不 panic
//  2. GetMerchantProfile 返回 nil 时返回 401，不 nil 解引用
//  3. AllowedChannels 从数据库获取，不硬编码 ["card","bank"]
//  4. 所有身份字段来自数据库，不接受请求体声明
//  5. 并发删除（profile 在 verify 后被删）稳定返回 401
func MerchantAuth(verifier MerchantVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// G13: verifier nil 检查
			if verifier == nil {
				WriteAPIError(w, apperr.New("MERCHANT_AUTH_UNAVAILABLE", http.StatusInternalServerError))
				return
			}

			merchantIDStr := r.Header.Get("X-Merchant-Id")
			signToken := r.Header.Get("X-Sign-Token")

			if merchantIDStr == "" || signToken == "" {
				WriteAPIError(w, apperr.New("MERCHANT_AUTH_REQUIRED", http.StatusUnauthorized))
				return
			}

			merchantID, err := uuid.Parse(merchantIDStr)
			if err != nil {
				WriteAPIError(w, apperr.New("MERCHANT_AUTH_FAILED", http.StatusUnauthorized))
				return
			}

			verified, err := verifier.VerifyMerchant(r.Context(), merchantID, signToken)
			if err != nil {
				// 数据库错误：503 fail-closed，不泄露内部错误
				WriteAPIError(w, apperr.New("MERCHANT_AUTH_UNAVAILABLE", http.StatusServiceUnavailable))
				return
			}
			if !verified {
				WriteAPIError(w, apperr.New("MERCHANT_AUTH_FAILED", http.StatusUnauthorized))
				return
			}

			mp, err := verifier.GetMerchantProfile(r.Context(), merchantID)
			if err != nil {
				// G13: 商户不存在/被删除/数据库错误 → 401（不区分，防存在性泄露）
				WriteAPIError(w, apperr.New("MERCHANT_AUTH_FAILED", http.StatusUnauthorized))
				return
			}
			// G13: nil profile 防护（即使 GetMerchantProfile 实现错误也不 panic）
			if mp == nil {
				WriteAPIError(w, apperr.New("MERCHANT_AUTH_FAILED", http.StatusUnauthorized))
				return
			}

			if mp.Status != "ACTIVE" {
				WriteAPIError(w, apperr.New("MERCHANT_NOT_ACTIVE", http.StatusForbidden))
				return
			}

			// G13: AllowedChannels 从数据库获取，不硬编码
			allowedChannels, _ := verifier.GetAllowedChannels(r.Context(), merchantID)
			if allowedChannels == nil {
				allowedChannels = []string{} // fail-closed：无配置则无通道
			}

			ctx := context.WithValue(r.Context(), merchantAuthKey{}, MerchantContext{
				MerchantID:      mp.ID,
				EntityID:        mp.EntityID,
				RiskProfileID:   mp.RiskProfileID,
				Status:          mp.Status,
				ProviderTier:    mp.ProviderTier,
				TenantID:        mp.TenantID,
				AllowedChannels: allowedChannels,
				AuthenticatedAt: time.Now(),
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func GetMerchantContext(ctx context.Context) (MerchantContext, bool) {
	mc, ok := ctx.Value(merchantAuthKey{}).(MerchantContext)
	return mc, ok
}

func WriteAPIError(w http.ResponseWriter, err *apperr.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.HTTP)
	_, _ = w.Write([]byte(`{"code":"` + err.Code + `","message":"` + err.Message + `"}`))
}
