package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	issuing "github.com/nexusglobal/mamr/internal/issuing"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type V3IssuingPoolsHandler struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
}

func NewV3IssuingPoolsHandler(pool *pgxpool.Pool, rdb *redis.Client) *V3IssuingPoolsHandler {
	return &V3IssuingPoolsHandler{pool: pool, rdb: rdb}
}

func (h *V3IssuingPoolsHandler) publishConfigUpdate(poolID uuid.UUID, version int) {
	if h.rdb != nil {
		payload := fmt.Sprintf("%s|%d", poolID.String(), version)
		h.rdb.Publish(context.Background(), "config:issuing_pool:reload", payload)
	}
}

func (h *V3IssuingPoolsHandler) ListPools(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT id::text, COALESCE(entity_id::text, ''), issuing_tier, funding_mode, provider_code, pool_code, 
		 status, version, provider_config::text, velocity_config::text, 
		 created_at::text, updated_at::text
		 FROM issuing_providers ORDER BY created_at DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, entityID, issuingTier, fundingMode, providerCode, poolCode, status, providerConfig, velocityConfig, created, updated string
		var version int
		if err := rows.Scan(&id, &entityID, &issuingTier, &fundingMode, &providerCode, &poolCode, &status, &version, &providerConfig, &velocityConfig, &created, &updated); err != nil {
			writeErr(w, err)
			return
		}

		items = append(items, map[string]interface{}{
			"id":              id,
			"entity_id":       entityID,
			"issuing_tier":    issuingTier,
			"funding_mode":    fundingMode,
			"provider_code":   providerCode,
			"pool_code":       poolCode,
			"status":          status,
			"version":         version,
			"provider_config": sanitizeJSONText(providerConfig),
			"velocity_config": sanitizeJSONText(velocityConfig),
			"created_at":      created,
			"updated_at":      updated,
			"has_credential":  true,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *V3IssuingPoolsHandler) CreatePool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntityID           string                 `json:"entity_id"`
		IssuingTier        string                 `json:"issuing_tier"`
		FundingMode        string                 `json:"funding_mode"`
		ProviderCode       string                 `json:"provider_code"`
		PoolCode           string                 `json:"pool_code"`
		CredentialVaultKey string                 `json:"credential_vault_key"`
		ProviderConfig     map[string]interface{} `json:"provider_config"`
		VelocityConfig     map[string]interface{} `json:"velocity_config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	if err := validateIssuingPoolRequest(&req); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()
	var entityUUID *uuid.UUID
	if req.EntityID != "" {
		id, _ := uuid.Parse(req.EntityID)
		entityUUID = &id
	}

	if req.ProviderConfig == nil {
		req.ProviderConfig = map[string]interface{}{}
	}
	if req.VelocityConfig == nil {
		req.VelocityConfig = map[string]interface{}{}
	}

	providerConfigJSON, _ := json.Marshal(req.ProviderConfig)
	velocityConfigJSON, _ := json.Marshal(req.VelocityConfig)

	var id uuid.UUID
	err := h.pool.QueryRow(ctx,
		`INSERT INTO issuing_providers (
			entity_id, issuing_tier, funding_mode, provider_code, pool_code, 
			credential_vault_key, provider_config, velocity_config
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		entityUUID, req.IssuingTier, req.FundingMode, req.ProviderCode, req.PoolCode,
		req.CredentialVaultKey, providerConfigJSON, velocityConfigJSON,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

func (h *V3IssuingPoolsHandler) UpdatePool(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid pool id"))
		return
	}

	var req struct {
		IssuingTier        string                 `json:"issuing_tier"`
		FundingMode        string                 `json:"funding_mode"`
		CredentialVaultKey string                 `json:"credential_vault_key"`
		ProviderConfig     map[string]interface{} `json:"provider_config"`
		VelocityConfig     map[string]interface{} `json:"velocity_config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	if req.IssuingTier != "" {
		if err := issuing.IssuingTier(req.IssuingTier).Validate(); err != nil {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid issuing_tier"))
			return
		}
	}

	if req.FundingMode != "" {
		if err := issuing.IssuingFundingMode(req.FundingMode).Validate(); err != nil {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid funding_mode"))
			return
		}
	}

	ctx := r.Context()
	var updates []string
	var args []interface{}
	argIdx := 1

	if req.IssuingTier != "" {
		updates = append(updates, "issuing_tier = $"+string(rune(argIdx+'0')))
		args = append(args, req.IssuingTier)
		argIdx++
	}
	if req.FundingMode != "" {
		updates = append(updates, "funding_mode = $"+string(rune(argIdx+'0')))
		args = append(args, req.FundingMode)
		argIdx++
	}
	if req.CredentialVaultKey != "" {
		updates = append(updates, "credential_vault_key = $"+string(rune(argIdx+'0')))
		args = append(args, req.CredentialVaultKey)
		argIdx++
	}
	if req.ProviderConfig != nil {
		configJSON, _ := json.Marshal(req.ProviderConfig)
		updates = append(updates, "provider_config = $"+string(rune(argIdx+'0')))
		args = append(args, configJSON)
		argIdx++
	}
	if req.VelocityConfig != nil {
		configJSON, _ := json.Marshal(req.VelocityConfig)
		updates = append(updates, "velocity_config = $"+string(rune(argIdx+'0')))
		args = append(args, configJSON)
		argIdx++
	}

	if len(updates) == 0 {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("no update fields provided"))
		return
	}

	updates = append(updates, "updated_at = now()")
	args = append(args, id)

	query := "UPDATE issuing_providers SET " + joinWithComma(updates) + ", version = version + 1 WHERE id = $" + string(rune(argIdx+'0')) + " RETURNING version"
	var newVersion int
	err = h.pool.QueryRow(ctx, query, args...).Scan(&newVersion)
	if err != nil {
		writeErr(w, err)
		return
	}

	h.publishConfigUpdate(id, newVersion)

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "updated", "version": newVersion})
}

func (h *V3IssuingPoolsHandler) EnablePool(w http.ResponseWriter, r *http.Request) {
	h.setPoolStatus(w, r, "ACTIVE")
}

func (h *V3IssuingPoolsHandler) DisablePool(w http.ResponseWriter, r *http.Request) {
	h.setPoolStatus(w, r, "INACTIVE")
}

func (h *V3IssuingPoolsHandler) ReloadPool(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid pool id"))
		return
	}

	ctx := r.Context()
	var newVersion int
	err = h.pool.QueryRow(ctx, `UPDATE issuing_providers SET version = version + 1 WHERE id = $1 RETURNING version`, id).Scan(&newVersion)
	if err != nil {
		writeErr(w, err)
		return
	}

	h.publishConfigUpdate(id, newVersion)

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "reload_triggered", "version": newVersion})
}

func (h *V3IssuingPoolsHandler) setPoolStatus(w http.ResponseWriter, r *http.Request, status string) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid pool id"))
		return
	}

	ctx := r.Context()
	_, err = h.pool.Exec(ctx, `UPDATE issuing_providers SET status = $1, updated_at = now() WHERE id = $2`, status, id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": status})
}

func validateIssuingPoolRequest(req *struct {
	EntityID           string                 `json:"entity_id"`
	IssuingTier        string                 `json:"issuing_tier"`
	FundingMode        string                 `json:"funding_mode"`
	ProviderCode       string                 `json:"provider_code"`
	PoolCode           string                 `json:"pool_code"`
	CredentialVaultKey string                 `json:"credential_vault_key"`
	ProviderConfig     map[string]interface{} `json:"provider_config"`
	VelocityConfig     map[string]interface{} `json:"velocity_config"`
}) error {
	if req.ProviderCode == "" {
		return apperr.ErrInvalidArgument.WithMessage("provider_code is required")
	}
	if req.CredentialVaultKey == "" {
		return apperr.ErrInvalidArgument.WithMessage("credential_vault_key is required")
	}
	if err := issuing.IssuingTier(req.IssuingTier).Validate(); err != nil {
		return apperr.ErrInvalidArgument.WithMessage("invalid issuing_tier")
	}
	if err := issuing.IssuingFundingMode(req.FundingMode).Validate(); err != nil {
		return apperr.ErrInvalidArgument.WithMessage("invalid funding_mode")
	}
	if req.ProviderConfig != nil {
		if _, err := json.Marshal(req.ProviderConfig); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("provider_config must be a valid JSON object")
		}
	}
	if req.VelocityConfig != nil {
		if _, err := json.Marshal(req.VelocityConfig); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("velocity_config must be a valid JSON object")
		}
	}
	return nil
}
