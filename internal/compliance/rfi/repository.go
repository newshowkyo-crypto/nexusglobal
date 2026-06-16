package rfi

import (
	"context"

	"github.com/google/uuid"
)

// Repository RFI 文档仓储接口
type Repository interface {
	CreateDocument(ctx context.Context, doc *RFIDocument) error
	GetDocumentByID(ctx context.Context, id uuid.UUID) (*RFIDocument, error)
	ListDocuments(ctx context.Context, merchantID *uuid.UUID, status *DocumentStatus) ([]*RFIDocument, error)
	UpdateDocumentStatus(ctx context.Context, id uuid.UUID, status DocumentStatus, operator string, reason *string) error
}

// OrderSourceProvider 订单源数据提供者接口
type OrderSourceProvider interface {
	GetOrderSourceData(ctx context.Context, orderID uuid.UUID) (*OrderSourceData, error)
}
