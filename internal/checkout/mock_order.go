package checkout

import (
	"context"
	"fmt"
)

// MockOrderProvider 本地模拟订单提供者，用于演示模式
type MockOrderProvider struct{}

func NewMockOrderProvider() *MockOrderProvider {
	return &MockOrderProvider{}
}

func (m *MockOrderProvider) GetOrderDetails(ctx context.Context, orderID string) (*CheckoutData, error) {
	if orderID == "" {
		return nil, fmt.Errorf("order ID is required")
	}

	// 根据 orderID 返回确定性的订单数据
	return &CheckoutData{
		OrderID:      orderID,
		MerchantName: "NexusGlobal Demo Merchant",
		Amount:       "99.99",
		Currency:     "USD",
		Descriptor: MerchantDescriptor{
			Name:         "NexusGlobal Demo Store",
			LogoURL:      "https://via.placeholder.com/150x50?text=NexusGlobal",
			TermsURL:     "/terms",
			RefundPolicy: "Refunds available within 30 days",
			SupportEmail: "support@nexusglobal.com",
			SupportPhone: "+1-555-0123",
		},
		SuccessURL: "/checkout/success",
		CancelURL:  "/checkout/cancel",
	}, nil
}
