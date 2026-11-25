package refund

import (
	"context"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/checkout"
)

// MockGateway 仅用于 local/mock 环境：不调用真实上游，直接返回成功。
// production/real 必须注入 checkout.AcquirerGateway，否则启动即 fail-closed（见 cmd/worker）。
type MockGateway struct{}

func (MockGateway) Refund(_ context.Context, req checkout.RefundRequest) (*checkout.RefundResult, error) {
	return &checkout.RefundResult{
		ProviderRef:    "mock_refund_" + uuid.NewString(),
		Status:         "succeeded",
		RefundedAmount: req.Amount,
	}, nil
}
