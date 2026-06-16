package adapter

import "errors"

// Provider 适配器统一错误类型
// 这些错误由具体 provider 适配器映射后返回给上层

var (
	// ErrPaymentDeclined 支付被拒绝
	ErrPaymentDeclined = errors.New("adapter: payment declined")

	// ErrPaymentRequiresAction 需要用户操作（如 3DS）
	ErrPaymentRequiresAction = errors.New("adapter: payment requires action")

	// ErrProviderTimeout 提供商超时
	ErrProviderTimeout = errors.New("adapter: provider timeout")

	// ErrProviderRateLimited 提供商限流
	ErrProviderRateLimited = errors.New("adapter: provider rate limited")

	// ErrProviderAuthFailed 提供商认证失败
	ErrProviderAuthFailed = errors.New("adapter: provider auth failed")

	// ErrProviderInvalidRequest 提供商请求无效
	ErrProviderInvalidRequest = errors.New("adapter: provider invalid request")

	// ErrUnknownExternalState 未知外部状态（超时、网络错误等）
	ErrUnknownExternalState = errors.New("adapter: unknown external state")

	// ErrWebhookSignatureInvalid Webhook 签名无效
	ErrWebhookSignatureInvalid = errors.New("adapter: webhook signature invalid")

	// ErrWebhookReplay Webhook 重放
	ErrWebhookReplay = errors.New("adapter: webhook replay")

	// ErrPaymentMethodTokenRequired 需要支付方法 token
	ErrPaymentMethodTokenRequired = errors.New("adapter: payment method token required")

	// ErrRawCardNotAllowed 不允许原始卡号
	ErrRawCardNotAllowed = errors.New("adapter: raw card not allowed")
)

// IsUnknownExternalState 判断是否为未知外部状态错误
func IsUnknownExternalState(err error) bool {
	return errors.Is(err, ErrUnknownExternalState) ||
		errors.Is(err, ErrProviderTimeout)
}

// IsExplicitDecline 判断是否为明确拒绝错误
func IsExplicitDecline(err error) bool {
	return errors.Is(err, ErrPaymentDeclined)
}

// IsRequiresAction 判断是否需要用户操作
func IsRequiresAction(err error) bool {
	return errors.Is(err, ErrPaymentRequiresAction)
}
