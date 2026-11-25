package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v4/pgxpool"
)

type MerchantProfile struct {
	ID            uuid.UUID
	EntityID      uuid.UUID
	RiskProfileID uuid.UUID
	Status        string
	ProviderTier  string
	TenantID      string
}

type MerchantRepository struct {
	pool *pgxpool.Pool
}

func NewMerchantRepository(pool *pgxpool.Pool) *MerchantRepository {
	return &MerchantRepository{pool: pool}
}

func (r *MerchantRepository) GetMerchantByID(ctx context.Context, merchantID uuid.UUID) (*MerchantProfile, error) {
	var mp MerchantProfile
	err := r.pool.QueryRow(ctx, `
		SELECT e.id, e.entity_id, rp.id, e.status, COALESCE(e.provider_tier, 'TIER_2_COMPLIANT'), COALESCE(e.tenant_id, '')
		FROM corporate_entity e
		LEFT JOIN merchant_risk_profiles rp ON e.id = rp.merchant_id
		WHERE e.id = $1
	`, merchantID).Scan(&mp.ID, &mp.EntityID, &mp.RiskProfileID, &mp.Status, &mp.ProviderTier, &mp.TenantID)
	if err != nil {
		return nil, err
	}
	return &mp, nil
}

// VerifySignToken 校验商户 sign token。
//
// G13 修复：
//   - 原实现 constantTimeCompare(tokenHash, signToken) 比较的是 hash 和明文 token，永远 false
//   - 修正为比较 storedHash 和 hashToken(signToken)
//   - 使用 crypto/subtle.ConstantTimeCompare 恒时比较
func (r *MerchantRepository) VerifySignToken(ctx context.Context, merchantID uuid.UUID, signToken string) (bool, error) {
	if signToken == "" {
		return false, nil
	}
	var storedHash string
	err := r.pool.QueryRow(ctx, `
		SELECT sign_token_hash FROM corporate_entity WHERE id = $1
	`, merchantID).Scan(&storedHash)
	if err != nil {
		return false, err
	}
	if storedHash == "" {
		return false, nil
	}
	providedHash := hashSignToken(signToken)
	if len(storedHash) != len(providedHash) {
		return false, nil
	}
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(providedHash)) == 1, nil
}

func hashSignToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
