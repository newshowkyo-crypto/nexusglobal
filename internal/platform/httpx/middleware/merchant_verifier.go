package middleware

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrMerchantNotFound 商户不存在时返回的稳定错误。
// 调用方应将其映射为 401（不要泄露"商户不存在"给外部）。
var ErrMerchantNotFound = errors.New("merchant not found")

// ErrMerchantProfileEmpty 商户 profile 为空时返回的稳定错误。
var ErrMerchantProfileEmpty = errors.New("merchant profile empty")

type DBMerchantVerifier struct {
	pool *pgxpool.Pool
}

func NewDBMerchantVerifier(pool *pgxpool.Pool) *DBMerchantVerifier {
	return &DBMerchantVerifier{pool: pool}
}

// VerifyMerchant 校验商户 sign token。
//
// 安全性 (G13)：
//   - signToken 为空直接拒绝
//   - 商户不存在返回 false, nil（不泄露存在性）
//   - storedHash 为空返回 false, nil（未配置 sign token）
//   - 使用 crypto/subtle.ConstantTimeCompare 恒时比较，防时序攻击
//   - 数据库错误返回 false, err（调用方决定 500/503）
func (v *DBMerchantVerifier) VerifyMerchant(ctx context.Context, merchantID uuid.UUID, signToken string) (bool, error) {
	if v.pool == nil {
		return false, fmt.Errorf("merchant verifier: database pool not configured")
	}
	if signToken == "" {
		return false, nil
	}
	tokenHash := hashToken(signToken)
	var storedHash string
	err := v.pool.QueryRow(ctx, `
		SELECT sign_token_hash FROM corporate_entity WHERE id = $1
	`, merchantID).Scan(&storedHash)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	// 如果商户未设置 sign_token_hash，则不允许认证
	if storedHash == "" {
		return false, nil
	}
	// G13: 使用 crypto/subtle 恒时比较，防时序攻击
	if len(storedHash) != len(tokenHash) {
		return false, nil
	}
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(tokenHash)) == 1, nil
}

func (v *DBMerchantVerifier) IsMerchantActive(ctx context.Context, merchantID uuid.UUID) (bool, error) {
	if v.pool == nil {
		return false, fmt.Errorf("merchant verifier: database pool not configured")
	}
	var status string
	err := v.pool.QueryRow(ctx, `
		SELECT status FROM corporate_entity WHERE id = $1
	`, merchantID).Scan(&status)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return status == "ACTIVE", nil
}

// GetMerchantProfile 获取商户 profile。
//
// G13 修复：原先在 pgx.ErrNoRows 时返回 (nil, nil)，导致调用方
// 对 nil profile 解引用 panic。现在返回 (nil, ErrMerchantNotFound)，
// 调用方可以稳定映射为 401/403。
func (v *DBMerchantVerifier) GetMerchantProfile(ctx context.Context, merchantID uuid.UUID) (*MerchantProfile, error) {
	if v.pool == nil {
		return nil, fmt.Errorf("merchant verifier: database pool not configured")
	}
	var mp MerchantProfile
	var riskProfileID pgtype.UUID
	var tenantID uuid.UUID

	// EntityID 直接使用 corporate_entity.id
	// ProviderTier 从 corporate_entity.provider_tier 获取，默认 TIER_2_COMPLIANT
	// RiskProfileID 从 merchant_risk_profiles 获取，允许为空
	err := v.pool.QueryRow(ctx, `
		SELECT
			e.id,
			e.id AS entity_id,
			e.status,
			COALESCE(e.provider_tier, 'TIER_2_COMPLIANT'),
			e.tenant_id,
			rp.id
		FROM corporate_entity e
		LEFT JOIN merchant_risk_profiles rp ON e.id = rp.merchant_id
		WHERE e.id = $1
	`, merchantID).Scan(&mp.ID, &mp.EntityID, &mp.Status, &mp.ProviderTier, &tenantID, &riskProfileID)
	if err != nil {
		if err == pgx.ErrNoRows {
			// G13: 返回明确错误，而非 (nil, nil)
			return nil, ErrMerchantNotFound
		}
		return nil, err
	}

	// G13: 校验 profile 非空
	if mp.Status == "" {
		return nil, ErrMerchantProfileEmpty
	}

	// RiskProfileID 允许为空 UUID（没有 risk profile 记录时）
	if riskProfileID.Valid {
		mp.RiskProfileID = riskProfileID.Bytes
	} else {
		mp.RiskProfileID = uuid.Nil
	}
	mp.TenantID = tenantID.String()

	return &mp, nil
}

// GetAllowedChannels 从数据库获取商户授权的通道列表。
//
// G13 修复：原先 AllowedChannels 硬编码为 ["card", "bank"]，
// 所有商户默认拥有全部通道权限。现在从 merchant_allowed_channels
// 表读取，未配置则返回空切片（最小权限）。
//
// 如果表不存在或查询失败，返回空切片而非 panic（fail-closed）。
func (v *DBMerchantVerifier) GetAllowedChannels(ctx context.Context, merchantID uuid.UUID) ([]string, error) {
	if v.pool == nil {
		return nil, nil
	}
	rows, err := v.pool.Query(ctx, `
		SELECT channel_code
		FROM merchant_allowed_channels
		WHERE merchant_id = $1 AND status = 'ACTIVE'
		ORDER BY channel_code
	`, merchantID)
	if err != nil {
		// 表可能不存在或暂时不可用；fail-closed 返回空
		return nil, nil
	}
	defer rows.Close()

	var channels []string
	for rows.Next() {
		var ch string
		if err := rows.Scan(&ch); err == nil && ch != "" {
			channels = append(channels, ch)
		}
	}
	if channels == nil {
		channels = []string{}
	}
	return channels, nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
