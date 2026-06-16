package rfi

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Service RFI 文档服务
type Service struct {
	repo           Repository
	sourceProvider OrderSourceProvider
	clock          func() time.Time
}

// NewService 创建服务实例
func NewService(repo Repository, sourceProvider OrderSourceProvider, clock func() time.Time) *Service {
	return &Service{
		repo:           repo,
		sourceProvider: sourceProvider,
		clock:          clock,
	}
}

// GenerateDocument 生成 RFI 文档
func (s *Service) GenerateDocument(ctx context.Context, orderID uuid.UUID, docType DocumentType) (*RFIDocument, error) {
	// 获取订单源数据
	sourceData, err := s.sourceProvider.GetOrderSourceData(ctx, orderID)
	if err != nil {
		return nil, err
	}

	if sourceData == nil {
		return nil, ErrOrderNotFound
	}

	// 检查源数据完整性
	hasCompleteData := sourceData.HasCompleteSourceData(docType)
	sourceHash := GenerateSourceHash(sourceData)

	// 确定文档状态
	status := DocumentStatusPending
	if !hasCompleteData {
		status = DocumentStatusReviewRequired
	}

	// 生成 PDF 内容（模拟）
	pdfContent := s.generatePDFContent(sourceData, docType)

	doc := &RFIDocument{
		ID:             uuid.New(),
		OrderID:        orderID,
		MerchantID:     uuid.MustParse(sourceData.MerchantID),
		DocumentType:   docType,
		SourceDataHash: sourceHash,
		Status:         status,
		PDFContent:     pdfContent,
		Metadata: map[string]interface{}{
			"order_id":         sourceData.OrderID,
			"merchant_id":      sourceData.MerchantID,
			"source_data_hash": sourceHash,
			"document_type":    string(docType),
		},
		CreatedAt: s.clock(),
		UpdatedAt: s.clock(),
	}

	err = s.repo.CreateDocument(ctx, doc)
	if err != nil {
		return nil, err
	}

	return doc, nil
}

// GetDocument 获取文档详情
func (s *Service) GetDocument(ctx context.Context, id uuid.UUID) (*RFIDocument, error) {
	return s.repo.GetDocumentByID(ctx, id)
}

// ListDocuments 列出文档
func (s *Service) ListDocuments(ctx context.Context, merchantID *uuid.UUID, status *DocumentStatus) ([]*RFIDocument, error) {
	return s.repo.ListDocuments(ctx, merchantID, status)
}

// ApproveDocument 审批文档
func (s *Service) ApproveDocument(ctx context.Context, id uuid.UUID, operator string) error {
	return s.repo.UpdateDocumentStatus(ctx, id, DocumentStatusApproved, operator, nil)
}

// RejectDocument 拒绝文档
func (s *Service) RejectDocument(ctx context.Context, id uuid.UUID, operator string, reason string) error {
	return s.repo.UpdateDocumentStatus(ctx, id, DocumentStatusRejected, operator, &reason)
}

// generatePDFContent 生成 PDF 内容（模拟实现）
func (s *Service) generatePDFContent(sourceData *OrderSourceData, docType DocumentType) []byte {
	content := []byte("RFI Document\n")
	content = append(content, []byte("Order ID: "+sourceData.OrderID+"\n")...)
	content = append(content, []byte("Merchant ID: "+sourceData.MerchantID+"\n")...)
	content = append(content, []byte("Type: "+string(docType)+"\n")...)
	content = append(content, []byte("Generated at: "+s.clock().String()+"\n")...)
	return content
}
