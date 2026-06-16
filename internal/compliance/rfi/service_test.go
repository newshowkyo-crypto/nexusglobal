package rfi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// mockRepository 用于测试的mock仓储
type mockRepository struct {
	docs []*RFIDocument
}

func (m *mockRepository) CreateDocument(ctx context.Context, doc *RFIDocument) error {
	m.docs = append(m.docs, doc)
	return nil
}

func (m *mockRepository) GetDocumentByID(ctx context.Context, id uuid.UUID) (*RFIDocument, error) {
	for _, d := range m.docs {
		if d.ID == id {
			return d, nil
		}
	}
	return nil, ErrDocumentNotFound
}

func (m *mockRepository) ListDocuments(ctx context.Context, merchantID *uuid.UUID, status *DocumentStatus) ([]*RFIDocument, error) {
	var result []*RFIDocument
	for _, d := range m.docs {
		if merchantID != nil && d.MerchantID != *merchantID {
			continue
		}
		if status != nil && d.Status != *status {
			continue
		}
		result = append(result, d)
	}
	return result, nil
}

func (m *mockRepository) UpdateDocumentStatus(ctx context.Context, id uuid.UUID, status DocumentStatus, operator string, reason *string) error {
	for _, d := range m.docs {
		if d.ID == id {
			d.Status = status
			d.UpdatedAt = time.Now()
			if status == DocumentStatusApproved {
				d.ApprovedBy = operator
				now := time.Now()
				d.ApprovedAt = &now
			} else if status == DocumentStatusRejected {
				d.RejectedBy = operator
				now := time.Now()
				d.RejectedAt = &now
				d.RejectedReason = *reason
			}
			return nil
		}
	}
	return ErrDocumentNotFound
}

// mockSourceProvider 用于测试的mock源数据提供者
type mockSourceProvider struct {
	data map[uuid.UUID]*OrderSourceData
}

func (m *mockSourceProvider) GetOrderSourceData(ctx context.Context, orderID uuid.UUID) (*OrderSourceData, error) {
	data, ok := m.data[orderID]
	if !ok {
		return nil, ErrOrderNotFound
	}
	return data, nil
}

func TestService_GenerateDocument(t *testing.T) {
	mockRepo := &mockRepository{}
	mockSource := &mockSourceProvider{
		data: map[uuid.UUID]*OrderSourceData{},
	}
	clock := func() time.Time { return time.Now() }
	service := NewService(mockRepo, mockSource, clock)

	orderID := uuid.New()
	merchantID := uuid.New()

	// 测试订单不存在
	_, err := service.GenerateDocument(context.Background(), orderID, DocumentTypeInvoice)
	if err != ErrOrderNotFound {
		t.Errorf("expected ErrOrderNotFound, got %v", err)
	}

	// 添加完整源数据
	mockSource.data[orderID] = &OrderSourceData{
		OrderID:    orderID.String(),
		MerchantID: merchantID.String(),
		Amount:     "1000",
		Currency:   "USD",
		InvoiceData: &InvoiceSourceData{
			InvoiceNumber: "INV-001",
			IssueDate:     "2024-01-01",
		},
	}

	// 测试生成文档
	doc, err := service.GenerateDocument(context.Background(), orderID, DocumentTypeInvoice)
	if err != nil {
		t.Fatalf("GenerateDocument failed: %v", err)
	}

	if doc.ID == uuid.Nil {
		t.Error("expected non-nil ID")
	}

	if doc.Status != DocumentStatusPending {
		t.Errorf("expected status PENDING, got %v", doc.Status)
	}

	if doc.SourceDataHash == "" {
		t.Error("expected non-empty source data hash")
	}
}

func TestService_GenerateDocument_MissingSourceData(t *testing.T) {
	mockRepo := &mockRepository{}
	mockSource := &mockSourceProvider{
		data: map[uuid.UUID]*OrderSourceData{},
	}
	clock := func() time.Time { return time.Now() }
	service := NewService(mockRepo, mockSource, clock)

	orderID := uuid.New()
	merchantID := uuid.New()

	// 添加不完整源数据（缺少发票数据）
	mockSource.data[orderID] = &OrderSourceData{
		OrderID:    orderID.String(),
		MerchantID: merchantID.String(),
		Amount:     "1000",
		Currency:   "USD",
		InvoiceData: &InvoiceSourceData{
			// 缺少 InvoiceNumber
			IssueDate: "2024-01-01",
		},
	}

	// 测试生成文档应该需要审查
	doc, err := service.GenerateDocument(context.Background(), orderID, DocumentTypeInvoice)
	if err != nil {
		t.Fatalf("GenerateDocument failed: %v", err)
	}

	if doc.Status != DocumentStatusReviewRequired {
		t.Errorf("expected status REVIEW_REQUIRED, got %v", doc.Status)
	}
}

func TestService_ApproveRejectDocument(t *testing.T) {
	mockRepo := &mockRepository{}
	mockSource := &mockSourceProvider{
		data: map[uuid.UUID]*OrderSourceData{},
	}
	clock := func() time.Time { return time.Now() }
	service := NewService(mockRepo, mockSource, clock)

	orderID := uuid.New()
	merchantID := uuid.New()
	mockSource.data[orderID] = &OrderSourceData{
		OrderID:    orderID.String(),
		MerchantID: merchantID.String(),
		InvoiceData: &InvoiceSourceData{
			InvoiceNumber: "INV-001",
		},
	}

	doc, _ := service.GenerateDocument(context.Background(), orderID, DocumentTypeInvoice)

	// 测试审批
	err := service.ApproveDocument(context.Background(), doc.ID, "admin")
	if err != nil {
		t.Fatalf("ApproveDocument failed: %v", err)
	}

	// 验证状态
	updatedDoc, _ := mockRepo.GetDocumentByID(context.Background(), doc.ID)
	if updatedDoc.Status != DocumentStatusApproved {
		t.Errorf("expected status APPROVED, got %v", updatedDoc.Status)
	}

	// 测试拒绝
	err = service.RejectDocument(context.Background(), doc.ID, "admin", "invalid data")
	if err != nil {
		t.Fatalf("RejectDocument failed: %v", err)
	}

	updatedDoc, _ = mockRepo.GetDocumentByID(context.Background(), doc.ID)
	if updatedDoc.Status != DocumentStatusRejected {
		t.Errorf("expected status REJECTED, got %v", updatedDoc.Status)
	}
	if updatedDoc.RejectedReason != "invalid data" {
		t.Errorf("expected reason 'invalid data', got %v", updatedDoc.RejectedReason)
	}
}
