package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/checkout"
	"github.com/nexusglobal/mamr/internal/checkout/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type PaymentLinksHandler struct {
	pool           *pgxpool.Pool
	paymentLinkSvc *checkout.PaymentLinkService
}

func NewPaymentLinksHandler(pool *pgxpool.Pool, _ interface{}) *PaymentLinksHandler {
	return &PaymentLinksHandler{
		pool:           pool,
		paymentLinkSvc: checkout.NewPaymentLinkService(pool),
	}
}

// NewPaymentLinksHandlerWithService 使用外部传入的 PaymentLinkService 构造 handler。
// P0-C2: Admin Handler 和公开 Confirm Handler 必须共享同一个带 Encryptor 的 Service 实例。
func NewPaymentLinksHandlerWithService(pool *pgxpool.Pool, paymentLinkSvc *checkout.PaymentLinkService) *PaymentLinksHandler {
	return &PaymentLinksHandler{
		pool:           pool,
		paymentLinkSvc: paymentLinkSvc,
	}
}

func (h *PaymentLinksHandler) CreatePaymentLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MerchantID      string               `json:"merchant_id"`
		MerchantOrderID string               `json:"merchant_order_id"`
		AmountMinor     int64                `json:"amount_minor"`
		Currency        string               `json:"currency"`
		SuccessURL      string               `json:"success_url"`
		CancelURL       string               `json:"cancel_url"`
		SingleUse       bool                 `json:"single_use,omitempty"`
		ExpiresAt       *time.Time           `json:"expires_at,omitempty"`
		Customer        *domain.CustomerInfo `json:"customer,omitempty"`
		Metadata        map[string]string    `json:"metadata,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{"code": "INVALID_REQUEST", "message": err.Error()},
		})
		return
	}

	merchantID, err := uuid.Parse(req.MerchantID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{"code": "INVALID_MERCHANT_ID", "message": "Invalid merchant ID"},
		})
		return
	}

	if req.MerchantOrderID == "" || req.AmountMinor <= 0 || req.Currency == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{"code": "INVALID_ARGUMENT", "message": "Missing required fields"},
		})
		return
	}

	result, err := h.paymentLinkSvc.CreatePaymentLink(r.Context(), checkout.CreatePaymentLinkRequest{
		MerchantID:      merchantID,
		MerchantOrderID: req.MerchantOrderID,
		AmountMinor:     req.AmountMinor,
		Currency:        req.Currency,
		SuccessURL:      req.SuccessURL,
		CancelURL:       req.CancelURL,
		SingleUse:       req.SingleUse,
		ExpiresAt:       req.ExpiresAt,
		Customer:        req.Customer,
		Metadata:        req.Metadata,
	})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if appErr, ok := apperr.AsAPIError(err); ok {
			w.WriteHeader(appErr.HTTP)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{"code": appErr.Code, "message": appErr.Message},
			})
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{"code": "INTERNAL_ERROR", "message": err.Error()},
			})
		}
		return
	}

	// G09: 审计日志不记录 token（只记录 payment_link_id 和非敏感信息）
	if err := h.writeAdminAuditLog(r.Context(), "CREATE_PAYMENT_LINK", map[string]interface{}{
		"payment_link_id": result.PaymentLinkID,
		"merchant_id":     req.MerchantID,
		"amount_minor":    req.AmountMinor,
		"currency":        req.Currency,
		"single_use":      req.SingleUse,
	}); err != nil {
		// NF-07: 不吞错误，显式记录。审计失败不阻塞业务返回（已创建的 link 不可撤回）。
		slog.ErrorContext(r.Context(), "admin audit log write failed",
			"action", "CREATE_PAYMENT_LINK",
			"payment_link_id", result.PaymentLinkID,
			"err", err)
	}

	// G09: 响应头禁止缓存（token 只返回一次）
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusCreated)
	// G09: token 和 payment_url 只返回一次，不落库
	json.NewEncoder(w).Encode(map[string]interface{}{
		"payment_link_id": result.PaymentLinkID,
		"token":           result.Token,
		"payment_url":     result.PaymentURL,
	})
}

// writeAdminAuditLog 写入统一审计日志（admin_audit_log，000034）
//
// NF-07: 不得吞掉审计写入错误。错误通过 log 返回，由调用方决定是否影响主流程。
// 审计失败不阻塞业务返回，但必须显式记录（不静默丢弃）。
func (h *PaymentLinksHandler) writeAdminAuditLog(ctx context.Context, action string, details map[string]interface{}) error {
	_, err := h.pool.Exec(ctx, `
		INSERT INTO admin_audit_log (action, details)
		VALUES ($1, $2)
	`, action, details)
	if err != nil {
		return fmt.Errorf("admin: write audit log: %w", err)
	}
	return nil
}
