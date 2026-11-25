package rfi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// DocumentType 文档类型
type DocumentType string

const (
	DocumentTypeInvoice         DocumentType = "INVOICE"
	DocumentTypeContract        DocumentType = "CONTRACT"
	DocumentTypeShipping        DocumentType = "SHIPPING"
	DocumentTypeProofOfDelivery DocumentType = "PROOF_OF_DELIVERY"
)

// DocumentStatus 文档状态
type DocumentStatus string

const (
	DocumentStatusPending        DocumentStatus = "PENDING"
	DocumentStatusReviewRequired DocumentStatus = "REVIEW_REQUIRED"
	DocumentStatusApproved       DocumentStatus = "APPROVED"
	DocumentStatusRejected       DocumentStatus = "REJECTED"
)

// RFIDocument RFI 合规文档
type RFIDocument struct {
	ID              uuid.UUID
	OrderID         uuid.UUID
	MerchantID      uuid.UUID
	DocumentType    DocumentType
	SourceDataHash  string
	Status          DocumentStatus
	DocumentPayload []byte
	Metadata        map[string]interface{}
	ApprovedBy      string
	ApprovedAt      *time.Time
	RejectedBy      string
	RejectedAt      *time.Time
	RejectedReason  string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// OrderSourceData 订单源数据
type OrderSourceData struct {
	OrderID      string              `json:"order_id"`
	MerchantID   string              `json:"merchant_id"`
	Amount       string              `json:"amount"`
	Currency     string              `json:"currency"`
	CreatedAt    string              `json:"created_at"`
	InvoiceData  *InvoiceSourceData  `json:"invoice_data,omitempty"`
	ContractData *ContractSourceData `json:"contract_data,omitempty"`
	ShippingData *ShippingSourceData `json:"shipping_data,omitempty"`
	DeliveryData *DeliverySourceData `json:"delivery_data,omitempty"`
}

// InvoiceSourceData 发票源数据
type InvoiceSourceData struct {
	InvoiceNumber string        `json:"invoice_number"`
	IssueDate     string        `json:"issue_date"`
	DueDate       string        `json:"due_date"`
	Items         []InvoiceItem `json:"items"`
}

// InvoiceItem 发票项目
type InvoiceItem struct {
	Description string `json:"description"`
	Quantity    int    `json:"quantity"`
	UnitPrice   string `json:"unit_price"`
	Total       string `json:"total"`
}

// ContractSourceData 合同源数据
type ContractSourceData struct {
	ContractNumber  string          `json:"contract_number"`
	EffectiveDate   string          `json:"effective_date"`
	TerminationDate string          `json:"termination_date"`
	Parties         []ContractParty `json:"parties"`
}

// ContractParty 合同方
type ContractParty struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// ShippingSourceData 物流源数据
type ShippingSourceData struct {
	TrackingNumber string `json:"tracking_number"`
	Carrier        string `json:"carrier"`
	Origin         string `json:"origin"`
	Destination    string `json:"destination"`
	ShippedAt      string `json:"shipped_at"`
	Status         string `json:"status"`
}

// DeliverySourceData 履约源数据
type DeliverySourceData struct {
	DeliveryDate  string `json:"delivery_date"`
	Recipient     string `json:"recipient"`
	Signature     string `json:"signature"`
	ProofImageURL string `json:"proof_image_url"`
}

// 错误定义
var (
	ErrDocumentNotFound    = errors.New("RFI document not found")
	ErrOrderNotFound       = errors.New("order not found")
	ErrMissingSourceData   = errors.New("missing required source data")
	ErrInvalidDocumentType = errors.New("invalid document type")
)

// GenerateSourceHash 生成源数据哈希
func GenerateSourceHash(data *OrderSourceData) string {
	jsonData, _ := json.Marshal(data)
	hash := sha256.Sum256(jsonData)
	return hex.EncodeToString(hash[:])
}

// HasCompleteSourceData 检查源数据是否完整
func (d *OrderSourceData) HasCompleteSourceData(docType DocumentType) bool {
	switch docType {
	case DocumentTypeInvoice:
		return d.InvoiceData != nil && d.InvoiceData.InvoiceNumber != ""
	case DocumentTypeContract:
		return d.ContractData != nil && d.ContractData.ContractNumber != ""
	case DocumentTypeShipping:
		return d.ShippingData != nil && d.ShippingData.TrackingNumber != ""
	case DocumentTypeProofOfDelivery:
		return d.DeliveryData != nil && d.DeliveryData.DeliveryDate != ""
	}
	return false
}
