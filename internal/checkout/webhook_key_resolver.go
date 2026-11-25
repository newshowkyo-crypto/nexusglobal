// webhook_key_resolver.go — SEC-007 的 per-provider 验签密钥解析器。
//
// 密钥来源：provider_credential_vault（credential_type='WEBHOOK_SECRET'）。
// 凭证由 internal/acquirer RotateSecret 写入，AES-256-GCM 加密，
// AAD 绑定 tenant + channel + field（crypto.Meta）。
//
// 解析优先级：
//  1. channel 级密钥：provider_id 匹配 payment_provider.provider_code 的
//     channel 绑定凭证（channel_id NOT NULL）。
//  2. provider 级密钥：channel_id IS NULL 的 provider 级凭证。
//
// 未找到任何 WEBHOOK_SECRET → ErrNoProviderKey（调用方 fail closed）。
package checkout

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/platform/crypto"
)

// DBSigningKeyResolver 从 provider_credential_vault 解析 per-provider 验签密钥。
type DBSigningKeyResolver struct {
	pool      *pgxpool.Pool
	encryptor *crypto.Encryptor
}

// NewDBSigningKeyResolver 创建解析器。encryptor 必须与写入凭证时使用的一致。
func NewDBSigningKeyResolver(pool *pgxpool.Pool, encryptor *crypto.Encryptor) *DBSigningKeyResolver {
	return &DBSigningKeyResolver{pool: pool, encryptor: encryptor}
}

// credentialRow 是解密所需的行投影。
type credentialRow struct {
	Ciphertext []byte
	TenantID   *uuid.UUID
	ChannelID  *uuid.UUID // nil 表示 provider 级
	KeyVersion int
}

// SigningKey 实现 checkout.SigningKeyResolver。
// providerID 语义：dispatch 使用的 payment_provider.provider_code。
func (r *DBSigningKeyResolver) SigningKey(ctx context.Context, _, providerID string) ([]byte, error) {
	if r.pool == nil {
		return nil, errors.New("webhook key resolver: pool is nil")
	}

	// channel 级优先，provider 级兜底（NULLS LAST 已由 ORDER BY 保证 channel 优先）
	rows, err := r.pool.Query(ctx, `
		SELECT pcv.ciphertext, c.tenant_id, c.id, pcv.key_version
		FROM payment_provider p
		JOIN provider_credential_vault pcv
		  ON pcv.provider_id = p.id AND pcv.credential_type = 'WEBHOOK_SECRET'
		LEFT JOIN tenant_acquirer_channel c ON c.id = pcv.channel_id
		WHERE p.provider_code = $1
		ORDER BY (pcv.channel_id IS NULL), pcv.key_version DESC
	`, providerID)
	if err != nil {
		return nil, fmt.Errorf("webhook key resolver: query: %w", err)
	}
	defer rows.Close()

	var candidates []credentialRow
	for rows.Next() {
		var c credentialRow
		if err := rows.Scan(&c.Ciphertext, &c.TenantID, &c.ChannelID, &c.KeyVersion); err != nil {
			return nil, fmt.Errorf("webhook key resolver: scan: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("webhook key resolver: rows: %w", err)
	}
	if len(candidates) == 0 {
		return nil, ErrNoProviderKey
	}

	// 逐个尝试解密（AAD 不匹配即跳过），全部失败则 fail closed。
	for _, c := range candidates {
		secret, decErr := r.decrypt(ctx, c)
		if decErr != nil {
			continue
		}
		return secret, nil
	}
	return nil, fmt.Errorf("webhook key resolver: decrypt credential failed for provider %q", providerID)
}

func (r *DBSigningKeyResolver) decrypt(ctx context.Context, c credentialRow) ([]byte, error) {
	if r.encryptor == nil {
		// 本地/测试场景可能未配置 encryptor：凭证以明文写入时直接返回。
		return c.Ciphertext, nil
	}
	if c.TenantID == nil || c.ChannelID == nil {
		return nil, errors.New("webhook key resolver: encrypted provider credential has no tenant/channel binding")
	}
	meta := crypto.Meta{
		TenantID:   c.TenantID.String(),
		RecordID:   c.ChannelID.String(),
		FieldName:  "WEBHOOK_SECRET",
		KeyVersion: fmt.Sprintf("%d", c.KeyVersion),
	}
	return r.encryptor.Decrypt(ctx, c.Ciphertext, meta)
}
