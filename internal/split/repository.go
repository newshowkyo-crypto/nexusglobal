// Package split 结算分账：按规则把商户净额分配给合作方。
package split

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Receiver 分账接收方（份额以 bps + 固定额表示）。
type Receiver struct {
	Account    string `json:"account"`
	RateBps    int    `json:"rate_bps"`
	FixedMinor int64  `json:"fixed_minor"`
}

// Rule 分账规则。
type Rule struct {
	ID         uuid.UUID  `json:"id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	MerchantID uuid.UUID  `json:"merchant_id"`
	Currency   string     `json:"currency"`
	Name       string     `json:"name"`
	Receivers  []Receiver `json:"receivers"`
	Status     string     `json:"status"`
	Version    int        `json:"version"`
}

// Share 单笔分账结果。
type Share struct {
	Receiver string `json:"receiver"`
	Amount   int64  `json:"amount_minor"`
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Resolve 选择商户+币种适用的规则（优先币种精确匹配）。
func (r *Repository) Resolve(ctx context.Context, merchantID uuid.UUID, currency string) (*Rule, error) {
	var rule Rule
	var raw []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, merchant_id, currency, name, receivers, status, version
		FROM split_rule
		WHERE merchant_id = $1 AND status = 'ACTIVE' AND (currency = '' OR currency = $2)
		ORDER BY (CASE WHEN currency = $2 THEN 1 ELSE 0 END) DESC, created_at DESC
		LIMIT 1
	`, merchantID, currency).Scan(&rule.ID, &rule.TenantID, &rule.MerchantID,
		&rule.Currency, &rule.Name, &raw, &rule.Status, &rule.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("split: resolve rule: %w", err)
	}
	if err := json.Unmarshal(raw, &rule.Receivers); err != nil {
		return nil, fmt.Errorf("split: decode receivers: %w", err)
	}
	return &rule, nil
}

// AllocateTx 按规则对单笔净额分账，返回总分账额。
// 幂等：split_record 按 (attempt, receiver) 唯一；重复结算返回首次结果。
func (r *Repository) AllocateTx(ctx context.Context, tx pgx.Tx, attemptID, merchantID uuid.UUID,
	currency string, batchID uuid.UUID, baseMinor int64) (int64, error) {
	rule, err := r.Resolve(ctx, merchantID, currency)
	if err != nil || rule == nil {
		return 0, err
	}

	shares := ComputeShares(baseMinor, rule.Receivers)
	var total int64
	for _, s := range shares {
		if s.Amount <= 0 {
			continue
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO split_record
			    (tenant_id, merchant_id, payment_attempt_id, settlement_batch_id, receiver, amount_minor, currency)
			VALUES ($1, $1, $2, $3, $4, $5, $6)
			ON CONFLICT (payment_attempt_id, receiver) DO NOTHING
		`, merchantID, attemptID, batchID, s.Receiver, s.Amount, currency)
		if err != nil {
			return 0, fmt.Errorf("split: insert record: %w", err)
		}
		if tag.RowsAffected() == 1 {
			total += s.Amount
			continue
		}
		// 已存在：以首次记录为准
		var existing int64
		if err := tx.QueryRow(ctx, `
			SELECT amount_minor FROM split_record
			WHERE payment_attempt_id = $1 AND receiver = $2
		`, attemptID, s.Receiver).Scan(&existing); err != nil {
			return 0, fmt.Errorf("split: read existing record: %w", err)
		}
		total += existing
	}
	return total, nil
}

// ComputeShares 计算各接收方份额；总和不超过 base。
func ComputeShares(base int64, receivers []Receiver) []Share {
	if base <= 0 || len(receivers) == 0 {
		return nil
	}
	out := make([]Share, 0, len(receivers))
	var used int64
	for _, rc := range receivers {
		amount := (base*int64(rc.RateBps) + 5000) / 10000
		amount += rc.FixedMinor
		if amount < 0 {
			amount = 0
		}
		if used+amount > base {
			amount = base - used
		}
		used += amount
		out = append(out, Share{Receiver: rc.Account, Amount: amount})
	}
	return out
}

// ListRules 列出商户分账规则。
func (r *Repository) ListRules(ctx context.Context, merchantID uuid.UUID) ([]Rule, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, merchant_id, currency, name, receivers, status, version
		FROM split_rule WHERE merchant_id = $1 ORDER BY created_at DESC
	`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Rule
	for rows.Next() {
		var rule Rule
		var raw []byte
		if err := rows.Scan(&rule.ID, &rule.TenantID, &rule.MerchantID, &rule.Currency,
			&rule.Name, &raw, &rule.Status, &rule.Version); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &rule.Receivers); err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// UpsertRule 创建分账规则。
func (r *Repository) UpsertRule(ctx context.Context, rule Rule) (uuid.UUID, error) {
	if rule.Status == "" {
		rule.Status = "ACTIVE"
	}
	raw, err := json.Marshal(rule.Receivers)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = r.pool.QueryRow(ctx, `
		INSERT INTO split_rule (id, tenant_id, merchant_id, currency, name, receivers, status, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 1)
		RETURNING id
	`, uuid.New(), rule.TenantID, rule.MerchantID, rule.Currency, rule.Name, raw, rule.Status).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("split: insert rule: %w", err)
	}
	return id, nil
}
