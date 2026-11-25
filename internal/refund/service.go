package refund

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/platform/postgres"
)

// Service 商户退款 API 服务。
type Service struct {
	pool *pgxpool.Pool
	txm  *postgres.TxManager
	repo *Repository
}

func NewService(pool *pgxpool.Pool, txm *postgres.TxManager, repo *Repository) *Service {
	return &Service{pool: pool, txm: txm, repo: repo}
}

// CreateCmd 创建退款请求。
type CreateCmd struct {
	MerchantID       uuid.UUID
	PaymentAttemptID uuid.UUID
	MerchantRefundID string
	IdempotencyKey   string
	AmountMinor      int64
	Reason           string
}

// Create 创建退款单并入队 REFUND_REQUESTED。
//
// 校验：
//   - attempt 必须属于该商户，且状态可退款；
//   - 累计退款（含在途）不得超过原支付金额；
//   - 幂等键相同且 payload 相同 → 返回已有退款；payload 不同 → 冲突。
func (s *Service) Create(ctx context.Context, cmd CreateCmd) (*Refund, error) {
	if cmd.MerchantID == uuid.Nil || cmd.PaymentAttemptID == uuid.Nil {
		return nil, fmt.Errorf("refund: merchant_id and payment_attempt_id are required")
	}
	if cmd.AmountMinor <= 0 {
		return nil, fmt.Errorf("refund: amount_minor must be positive")
	}
	if cmd.IdempotencyKey == "" {
		return nil, fmt.Errorf("refund: idempotency_key is required")
	}
	if cmd.MerchantRefundID == "" {
		cmd.MerchantRefundID = "refund_" + uuid.NewString()
	}

	var out *Refund
	err := s.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		existing, err := s.repo.GetByIdempotencyTx(ctx, tx, cmd.MerchantID, cmd.IdempotencyKey)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.PaymentAttemptID != cmd.PaymentAttemptID || existing.AmountMinor != cmd.AmountMinor {
				return ErrIdempotencyConflict
			}
			out = existing
			return nil
		}

		attempt, err := s.repo.LockAttemptTx(ctx, tx, cmd.PaymentAttemptID)
		if err != nil {
			return err
		}
		if attempt.MerchantID != cmd.MerchantID {
			return ErrAttemptNotFound
		}
		if !isRefundable(attempt.Status) {
			return fmt.Errorf("%w: status=%s", ErrNotRefundable, attempt.Status)
		}

		used, err := s.repo.SumActiveRefundsTx(ctx, tx, attempt.ID)
		if err != nil {
			return err
		}
		if used+cmd.AmountMinor > attempt.AmountMinor {
			return fmt.Errorf("%w: refundable=%d requested=%d",
				ErrAmountExceeds, attempt.AmountMinor-used, cmd.AmountMinor)
		}

		f := &Refund{
			ID:               uuid.New(),
			TenantID:         cmd.MerchantID,
			MerchantID:       cmd.MerchantID,
			PaymentAttemptID: attempt.ID,
			MerchantRefundID: cmd.MerchantRefundID,
			IdempotencyKey:   cmd.IdempotencyKey,
			AmountMinor:      cmd.AmountMinor,
			Currency:         attempt.Currency,
			Reason:           cmd.Reason,
		}
		if err := s.repo.InsertTx(ctx, tx, f); err != nil {
			return err
		}

		payload, _ := json.Marshal(map[string]string{"refund_request_id": f.ID.String()})
		if _, err := tx.Exec(ctx, `
			INSERT INTO transactional_outbox
			    (id, aggregate_type, aggregate_id, event_type, payload, idempotency_key, status)
			VALUES ($1, 'refund_request', $2, 'REFUND_REQUESTED', $3, $4, 'Pending')
		`, uuid.New(), f.ID, payload, "refund_requested_"+f.ID.String()); err != nil {
			return fmt.Errorf("refund: enqueue outbox: %w", err)
		}

		out = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get 查询退款单。
func (s *Service) Get(ctx context.Context, merchantID, id uuid.UUID) (*Refund, error) {
	return s.repo.Get(ctx, merchantID, id)
}

// List 列出商户退款单。
func (s *Service) List(ctx context.Context, merchantID uuid.UUID, limit int) ([]Refund, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.repo.ListByMerchant(ctx, merchantID, limit)
}

func isRefundable(status string) bool {
	switch status {
	case "CAPTURED", "SETTLED", "PARTIALLY_CAPTURED", "PARTIALLY_REFUNDED":
		return true
	default:
		return false
	}
}
