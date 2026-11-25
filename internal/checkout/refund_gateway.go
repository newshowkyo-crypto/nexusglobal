package checkout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	acquireradapter "github.com/nexusglobal/mamr/internal/acquirer/adapter"
	acquirerdomain "github.com/nexusglobal/mamr/internal/acquirer/domain"
)

// RefundRequest 商户退款请求（由 refund worker 调用）。
type RefundRequest struct {
	MerchantID        string
	AcquirerChannelID uuid.UUID
	ProviderRef       string
	Amount            decimal.Decimal
	Currency          string
	Reason            string
	IdempotencyKey    string
}

// RefundResult 退款结果。
type RefundResult struct {
	ProviderRef    string
	Status         string
	RefundedAmount decimal.Decimal
}

// Refund 调用上游退款。语义与 Charge 一致：
//   - 明确拒绝 → ErrPaymentFailed
//   - 超时/未知 → ErrPaymentTimeout（调用方不得自动重试）
func (g *AcquirerGateway) Refund(ctx context.Context, req RefundRequest) (*RefundResult, error) {
	if err := g.executionCheck(ctx); err != nil {
		return nil, ErrSystemLockdownFailClosed
	}
	if req.AcquirerChannelID == uuid.Nil {
		return nil, ErrChannelNotFound
	}
	merchantUUID, err := uuid.Parse(req.MerchantID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMerchantIDInvalid, err)
	}

	bundle, err := g.registry.Get(req.AcquirerChannelID)
	if err != nil {
		return nil, ErrChannelNotFound
	}
	if !bundle.IsActive {
		return nil, ErrChannelDisabled
	}
	if bundle.MerchantID != merchantUUID {
		return nil, ErrChannelNotFound
	}

	client, ok := bundle.Client.(acquirerdomain.AcquirerClient)
	if !ok {
		return nil, errors.New("acquirer gateway: invalid client type")
	}

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result, err := client.Refund(callCtx, acquirerdomain.RefundRequest{
		ChannelID:      req.AcquirerChannelID,
		ProviderRef:    req.ProviderRef,
		Amount:         req.Amount,
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		switch {
		case acquireradapter.IsUnknownExternalState(err):
			return nil, ErrPaymentTimeout
		case acquireradapter.IsExplicitDecline(err):
			return nil, ErrPaymentFailed
		default:
			return nil, err
		}
	}

	return &RefundResult{
		ProviderRef:    result.ProviderRef,
		Status:         string(result.Status),
		RefundedAmount: result.RefundedAmount,
	}, nil
}
