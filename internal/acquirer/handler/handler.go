package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/acquirer"
	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/platform/crypto"
)

// Handler 收单管理 handler
type Handler struct {
	svc       *acquirer.Service
	encryptor *crypto.Encryptor
}

// New 创建 handler
func New(svc *acquirer.Service, enc *crypto.Encryptor) *Handler {
	return &Handler{
		svc:       svc,
		encryptor: enc,
	}
}

// ListProvidersResponse 提供商列表响应
type ListProvidersResponse struct {
	Providers []ProviderResponse `json:"providers"`
}

// ProviderResponse 提供商响应
type ProviderResponse struct {
	ID           string `json:"id"`
	ProviderCode string `json:"provider_code"`
	Name         string `json:"name"`
	ProviderType string `json:"provider_type"`
	IsActive     bool   `json:"is_active"`
}

// ListProviders 列出所有提供商
func (h *Handler) ListProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := h.svc.ListProviders(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := ListProvidersResponse{
		Providers: make([]ProviderResponse, len(providers)),
	}
	for i, p := range providers {
		resp.Providers[i] = ProviderResponse{
			ID:           p.ID.String(),
			ProviderCode: p.ProviderCode,
			Name:         p.ProviderName,
			ProviderType: string(p.ProviderType),
			IsActive:     p.IsActive,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// CreateProviderRequest 创建提供商请求
type CreateProviderRequest struct {
	ProviderCode string `json:"provider_code"`
	Name         string `json:"name"`
	ProviderType string `json:"provider_type"`
}

// CreateProvider 创建提供商
func (h *Handler) CreateProvider(w http.ResponseWriter, r *http.Request) {
	var req CreateProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	provider, err := h.svc.CreateProvider(r.Context(), &domain.CreateProviderRequest{
		ProviderCode: req.ProviderCode,
		Name:         req.Name,
		ProviderType: req.ProviderType,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ProviderResponse{
		ID:           provider.ID.String(),
		ProviderCode: provider.ProviderCode,
		Name:         provider.ProviderName,
		ProviderType: string(provider.ProviderType),
		IsActive:     provider.IsActive,
	})
}

// ListChannelsResponse 通道列表响应
type ListChannelsResponse struct {
	Channels []ChannelResponse `json:"channels"`
}

// ChannelResponse 通道响应
type ChannelResponse struct {
	ID            string   `json:"id"`
	TenantID      string   `json:"tenant_id"`
	MerchantID    string   `json:"merchant_id"`
	ProviderID    string   `json:"provider_id"`
	ChannelCode   string   `json:"channel_code"`
	ChannelName   string   `json:"channel_name"`
	ProviderCode  string   `json:"provider_code,omitempty"`
	PaymentMethod string   `json:"payment_method"`
	Currencies    []string `json:"currencies"`
	Version       int      `json:"version"`
	IsActive      bool     `json:"is_active"`
	UpdatedAt     string   `json:"updated_at,omitempty"`
}

// ListChannels 列出所有通道
func (h *Handler) ListChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := h.svc.ListChannels(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := ListChannelsResponse{
		Channels: make([]ChannelResponse, len(channels)),
	}
	for i, c := range channels {
		resp.Channels[i] = ChannelResponse{
			ID:            c.ID.String(),
			TenantID:      c.TenantID.String(),
			MerchantID:    c.MerchantID.String(),
			ProviderID:    c.ProviderID.String(),
			ChannelCode:   c.ChannelCode,
			ChannelName:   c.ChannelName,
			PaymentMethod: c.PaymentMethod,
			Currencies:    c.Currencies,
			Version:       c.Version,
			IsActive:      c.IsActive,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// CreateChannelRequest 创建通道请求
type CreateChannelRequest struct {
	TenantID      string   `json:"tenant_id"`
	MerchantID    string   `json:"merchant_id"`
	ProviderID    string   `json:"provider_id"`
	ChannelCode   string   `json:"channel_code"`
	ChannelName   string   `json:"channel_name"`
	PaymentMethod string   `json:"payment_method"`
	Currencies    []string `json:"currencies"`
	IsActive      bool     `json:"is_active"`
}

// CreateChannel 创建通道
func (h *Handler) CreateChannel(w http.ResponseWriter, r *http.Request) {
	var req CreateChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	channel, err := h.svc.CreateChannel(r.Context(), &domain.CreateChannelRequest{
		TenantID:      req.TenantID,
		MerchantID:    req.MerchantID,
		ProviderID:    req.ProviderID,
		ChannelCode:   req.ChannelCode,
		ChannelName:   req.ChannelName,
		PaymentMethod: req.PaymentMethod,
		Currencies:    req.Currencies,
		IsActive:      req.IsActive,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ChannelResponse{
		ID:            channel.ID.String(),
		TenantID:      channel.TenantID.String(),
		MerchantID:    channel.MerchantID.String(),
		ProviderID:    channel.ProviderID.String(),
		ChannelCode:   channel.ChannelCode,
		ChannelName:   channel.ChannelName,
		PaymentMethod: channel.PaymentMethod,
		Currencies:    channel.Currencies,
		Version:       channel.Version,
		IsActive:      channel.IsActive,
	})
}

// UpdateChannelRequest 更新通道请求
type UpdateChannelRequest struct {
	ChannelName *string  `json:"channel_name,omitempty"`
	Currencies  []string `json:"currencies,omitempty"`
	IsActive    *bool    `json:"is_active,omitempty"`
}

// UpdateChannel 更新通道
func (h *Handler) UpdateChannel(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	var req UpdateChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err = h.svc.UpdateChannel(r.Context(), id, &domain.UpdateChannelRequest{
		ChannelName: req.ChannelName,
		Currencies:  req.Currencies,
		IsActive:    req.IsActive,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// EnableChannel 启用通道
func (h *Handler) EnableChannel(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	err = h.svc.EnableChannel(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "enabled"})
}

// DisableChannel 禁用通道
func (h *Handler) DisableChannel(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	err = h.svc.DisableChannel(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "disabled"})
}

// RotateSecretRequest 轮换密钥请求
type RotateSecretRequest struct {
	CredType string `json:"credential_type"`
}

// RotateSecret 轮换密钥
func (h *Handler) RotateSecret(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	var req RotateSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err = h.svc.RotateSecret(r.Context(), id, domain.CredentialType(req.CredType))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 不返回明文密钥，只返回状态
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":      "rotated",
		"key_version": "1",
	})
}

// ReloadChannel 重新加载通道
func (h *Handler) ReloadChannel(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	err = h.svc.ReloadChannel(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "reloaded"})
}
