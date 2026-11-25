package admin

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

var vaultKeyRegex = regexp.MustCompile(`^vault://`)

type V3MerchantHandler struct {
	pool *pgxpool.Pool
}

func NewV3MerchantHandler(pool *pgxpool.Pool) *V3MerchantHandler {
	return &V3MerchantHandler{pool: pool}
}

type CreateMerchantRequest struct {
	MerchantName          string `json:"merchant_name"`
	LegalName             string `json:"legal_name"`
	Jurisdiction          string `json:"jurisdiction"`
	SettlementCurrency    string `json:"settlement_currency"`
	WebhookURL            string `json:"webhook_url"`
	WebhookSecretVaultKey string `json:"webhook_secret_vault_key"`
	MAEConfigID           string `json:"mae_config_id"`
	BankAccountID         string `json:"bank_account_id"`
	ReserveRatioBps       int    `json:"reserve_ratio_bps"`
	RiskLabel             string `json:"risk_label"`
}

type MerchantResponse struct {
	ID                 string `json:"id"`
	MerchantName       string `json:"merchant_name"`
	LegalName          string `json:"legal_name"`
	Jurisdiction       string `json:"jurisdiction"`
	SettlementCurrency string `json:"settlement_currency"`
	WebhookURL         string `json:"webhook_url"`
	MAEConfigID        string `json:"mae_config_id"`
	BankAccountID      string `json:"bank_account_id"`
	ReserveRatioBps    int    `json:"reserve_ratio_bps"`
	RiskLabel          string `json:"risk_label"`
	CreatedAt          string `json:"created_at"`
}

// MerchantSafeResponse 只返回安全展示字段，不包含任何凭证。
// P0-2: ListMerchants 和 ListMerchantsForChannel 使用此结构。
type MerchantSafeResponse struct {
	ID           string `json:"id"`
	TenantID     string `json:"tenant_id"`
	MerchantName string `json:"merchant_name"`
	Status       string `json:"status"`
	Jurisdiction string `json:"jurisdiction,omitempty"`
	ProviderTier string `json:"provider_tier,omitempty"`
}

func (h *V3MerchantHandler) CreateMerchant(w http.ResponseWriter, r *http.Request) {
	var req CreateMerchantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	if req.WebhookSecretVaultKey != "" && !vaultKeyRegex.MatchString(req.WebhookSecretVaultKey) {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("webhook_secret_vault_key must start with 'vault://'"))
		return
	}

	if req.MerchantName == "" || req.LegalName == "" || req.Jurisdiction == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("merchant_name, legal_name, and jurisdiction are required"))
		return
	}

	merchantID := uuid.New()
	tenantID := uuid.New()
	if scopedTenantID, scoped := tenantScopeOf(r.Context()); scoped {
		var err error
		tenantID, err = uuid.Parse(scopedTenantID)
		if err != nil {
			writeErr(w, apperr.ErrForbidden.WithMessage("invalid tenant scope"))
			return
		}
	}

	// Extract country from jurisdiction (e.g., "US-CA" -> "US")
	country := req.Jurisdiction
	if idx := strings.Index(req.Jurisdiction, "-"); idx > 0 {
		country = req.Jurisdiction[:idx]
	}
	if len(country) > 2 {
		country = country[:2]
	}

	var metadata json.RawMessage
	metadata, _ = json.Marshal(map[string]interface{}{
		"webhook_url":              req.WebhookURL,
		"webhook_secret_vault_key": req.WebhookSecretVaultKey,
		"mae_config_id":            req.MAEConfigID,
		"bank_account_id":          req.BankAccountID,
		"reserve_ratio_bps":        req.ReserveRatioBps,
		"risk_label":               req.RiskLabel,
		"settlement_currency":      req.SettlementCurrency,
	})

	var createdAt string
	err := h.pool.QueryRow(r.Context(), `
		INSERT INTO corporate_entity (id, tenant_id, name, legal_name, jurisdiction, country, status, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, 'ACTIVE', $7)
		RETURNING created_at::text
	`, merchantID, tenantID, req.MerchantName, req.LegalName, req.Jurisdiction, country, metadata).Scan(&createdAt)
	if err != nil {
		writeErr(w, apperr.ErrInternal.WithMessage("failed to create merchant"))
		return
	}

	resp := MerchantResponse{
		ID:                 merchantID.String(),
		MerchantName:       req.MerchantName,
		LegalName:          req.LegalName,
		Jurisdiction:       req.Jurisdiction,
		SettlementCurrency: req.SettlementCurrency,
		WebhookURL:         req.WebhookURL,
		MAEConfigID:        req.MAEConfigID,
		BankAccountID:      req.BankAccountID,
		ReserveRatioBps:    req.ReserveRatioBps,
		RiskLabel:          req.RiskLabel,
		CreatedAt:          createdAt,
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, resp)
}

// P0-2: ListMerchants 只返回 id、tenant_id、name、status 和安全展示所需字段，不返回任何凭证。
func (h *V3MerchantHandler) ListMerchants(w http.ResponseWriter, r *http.Request) {
	// ADM-001: 对象级租户 scope
	query := `SELECT ce.id::text, ce.tenant_id::text, ce.name, ce.status, COALESCE(ce.jurisdiction, '')
		FROM corporate_entity ce`
	args := []interface{}{}
	if tenantID, scoped := tenantScopeOf(r.Context()); scoped {
		query += ` WHERE ce.tenant_id = $1`
		args = append(args, tenantID)
	}
	query += ` ORDER BY ce.created_at DESC`
	rows, err := h.pool.Query(r.Context(), query, args...)
	if err != nil {
		writeErr(w, apperr.ErrInternal.WithMessage("failed to list merchants"))
		return
	}
	defer rows.Close()

	var merchants []MerchantSafeResponse
	for rows.Next() {
		var id, tenantID, name, status, jurisdiction string
		if err := rows.Scan(&id, &tenantID, &name, &status, &jurisdiction); err != nil {
			writeErr(w, apperr.ErrInternal.WithMessage("failed to scan merchant"))
			return
		}
		merchants = append(merchants, MerchantSafeResponse{
			ID:           id,
			TenantID:     tenantID,
			MerchantName: name,
			Status:       status,
			Jurisdiction: jurisdiction,
		})
	}

	writeJSON(w, map[string]interface{}{
		"items": merchants,
		"total": len(merchants),
	})
}

// P0-2: ListMerchantsForChannel 只返回有 ACTIVE merchant_routing_policy 的商户，
// 供收单通道表单下拉框使用。禁止员工手填商户、实体或租户 UUID。
func (h *V3MerchantHandler) ListMerchantsForChannel(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT ce.id::text, ce.tenant_id::text, ce.name, ce.status, mrp.provider_tier
		FROM corporate_entity ce
		JOIN merchant_routing_policy mrp ON mrp.merchant_id = ce.id
		WHERE mrp.status = 'ACTIVE'
		ORDER BY ce.name
	`)
	if err != nil {
		writeErr(w, apperr.ErrInternal.WithMessage("failed to list merchants for channel"))
		return
	}
	defer rows.Close()

	var merchants []MerchantSafeResponse
	for rows.Next() {
		var id, tenantID, name, status, providerTier string
		if err := rows.Scan(&id, &tenantID, &name, &status, &providerTier); err != nil {
			writeErr(w, apperr.ErrInternal.WithMessage("failed to scan merchant"))
			return
		}
		merchants = append(merchants, MerchantSafeResponse{
			ID:           id,
			TenantID:     tenantID,
			MerchantName: name,
			Status:       status,
			ProviderTier: providerTier,
		})
	}

	writeJSON(w, map[string]interface{}{
		"items": merchants,
		"total": len(merchants),
	})
}

func getMetaString(meta map[string]interface{}, key string) string {
	if val, ok := meta[key]; ok {
		return val.(string)
	}
	return ""
}

func getMetaInt(meta map[string]interface{}, key string) int {
	if val, ok := meta[key]; ok {
		return int(val.(float64))
	}
	return 0
}
