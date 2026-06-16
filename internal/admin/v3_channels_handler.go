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

	"github.com/nexusglobal/mamr/internal/acquirer/domain"
	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type V3ChannelsHandler struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
}

func NewV3ChannelsHandler(pool *pgxpool.Pool, rdb *redis.Client) *V3ChannelsHandler {
	return &V3ChannelsHandler{pool: pool, rdb: rdb}
}

func (h *V3ChannelsHandler) publishConfigUpdate(channelID uuid.UUID, version int) {
	if h.rdb != nil {
		payload := fmt.Sprintf("%s|%d", channelID.String(), version)
		h.rdb.Publish(context.Background(), "channel.config.updated", payload)
		h.rdb.Publish(context.Background(), "config:acquirer_channel:reload", payload)
	}
}

func (h *V3ChannelsHandler) ListChannels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT id::text, entity_id::text, provider_tier, provider_code, channel_code, 
		 strategy_mode, status, version, provider_config::text, routing_config::text, 
		 created_at::text, updated_at::text
		 FROM acquirer_channels ORDER BY created_at DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var items []map[string]interface{}
	for rows.Next() {
		var id, entityID, providerTier, providerCode, channelCode, strategyMode, status, providerConfig, routingConfig, created, updated string
		var version int
		rows.Scan(&id, &entityID, &providerTier, &providerCode, &channelCode, &strategyMode, &status, &version, &providerConfig, &routingConfig, &created, &updated)

		items = append(items, map[string]interface{}{
			"id":              id,
			"entity_id":       entityID,
			"provider_tier":   providerTier,
			"provider_code":   providerCode,
			"channel_code":    channelCode,
			"strategy_mode":   strategyMode,
			"status":          status,
			"version":         version,
			"provider_config": providerConfig,
			"routing_config":  routingConfig,
			"created_at":      created,
			"updated_at":      updated,
			"has_credential":  true,
		})
	}
	writeJSON(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *V3ChannelsHandler) CreateChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntityID           string                 `json:"entity_id"`
		ProviderTier       string                 `json:"provider_tier"`
		ProviderCode       string                 `json:"provider_code"`
		ChannelCode        string                 `json:"channel_code"`
		CredentialVaultKey string                 `json:"credential_vault_key"`
		StrategyMode       string                 `json:"strategy_mode"`
		ProviderConfig     map[string]interface{} `json:"provider_config"`
		RoutingConfig      map[string]interface{} `json:"routing_config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	if err := validateChannelRequest(&req); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()
	entityUUID, _ := uuid.Parse(req.EntityID)

	if req.ProviderConfig == nil {
		req.ProviderConfig = map[string]interface{}{}
	}
	if req.RoutingConfig == nil {
		req.RoutingConfig = map[string]interface{}{}
	}

	providerConfigJSON, _ := json.Marshal(req.ProviderConfig)
	routingConfigJSON, _ := json.Marshal(req.RoutingConfig)

	var id uuid.UUID
	err := h.pool.QueryRow(ctx,
		`INSERT INTO acquirer_channels (
			entity_id, provider_tier, provider_code, channel_code, 
			credential_vault_key, strategy_mode, provider_config, routing_config
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		entityUUID, req.ProviderTier, req.ProviderCode, req.ChannelCode,
		req.CredentialVaultKey, req.StrategyMode, providerConfigJSON, routingConfigJSON,
	).Scan(&id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "created"})
}

func (h *V3ChannelsHandler) UpdateChannel(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid channel id"))
		return
	}

	var req struct {
		ProviderTier   string                 `json:"provider_tier"`
		StrategyMode   string                 `json:"strategy_mode"`
		ProviderConfig map[string]interface{} `json:"provider_config"`
		RoutingConfig  map[string]interface{} `json:"routing_config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}

	if req.ProviderTier != "" {
		if err := domain.ProviderTier(req.ProviderTier).Validate(); err != nil {
			writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid provider_tier"))
			return
		}
	}

	if req.StrategyMode != "" && req.StrategyMode != "DEFAULT" && req.StrategyMode != "BURN_MODE" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid strategy_mode"))
		return
	}

	ctx := r.Context()
	var updates []string
	var args []interface{}
	argIdx := 1

	if req.ProviderTier != "" {
		updates = append(updates, "provider_tier = $"+string(rune(argIdx+'0')))
		args = append(args, req.ProviderTier)
		argIdx++
	}
	if req.StrategyMode != "" {
		updates = append(updates, "strategy_mode = $"+string(rune(argIdx+'0')))
		args = append(args, req.StrategyMode)
		argIdx++
	}
	if req.ProviderConfig != nil {
		configJSON, _ := json.Marshal(req.ProviderConfig)
		updates = append(updates, "provider_config = $"+string(rune(argIdx+'0')))
		args = append(args, configJSON)
		argIdx++
	}
	if req.RoutingConfig != nil {
		configJSON, _ := json.Marshal(req.RoutingConfig)
		updates = append(updates, "routing_config = $"+string(rune(argIdx+'0')))
		args = append(args, configJSON)
		argIdx++
	}

	if len(updates) == 0 {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("no update fields provided"))
		return
	}

	updates = append(updates, "updated_at = now()")
	args = append(args, id)

	query := "UPDATE acquirer_channels SET " + joinWithComma(updates) + ", version = version + 1 WHERE id = $" + string(rune(argIdx+'0')) + " RETURNING version"
	var newVersion int
	err = h.pool.QueryRow(ctx, query, args...).Scan(&newVersion)
	if err != nil {
		writeErr(w, err)
		return
	}

	h.publishConfigUpdate(id, newVersion)

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "updated", "version": newVersion})
}

func (h *V3ChannelsHandler) EnableChannel(w http.ResponseWriter, r *http.Request) {
	h.setChannelStatus(w, r, "ACTIVE")
}

func (h *V3ChannelsHandler) DisableChannel(w http.ResponseWriter, r *http.Request) {
	h.setChannelStatus(w, r, "INACTIVE")
}

func (h *V3ChannelsHandler) ReloadChannel(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid channel id"))
		return
	}

	ctx := r.Context()
	var newVersion int
	err = h.pool.QueryRow(ctx, `UPDATE acquirer_channels SET version = version + 1 WHERE id = $1 RETURNING version`, id).Scan(&newVersion)
	if err != nil {
		writeErr(w, err)
		return
	}

	h.publishConfigUpdate(id, newVersion)

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": "reload_triggered", "version": newVersion})
}

func (h *V3ChannelsHandler) setChannelStatus(w http.ResponseWriter, r *http.Request, status string) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid channel id"))
		return
	}

	ctx := r.Context()
	_, err = h.pool.Exec(ctx, `UPDATE acquirer_channels SET status = $1, updated_at = now() WHERE id = $2`, status, id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, map[string]interface{}{"id": id.String(), "status": status})
}

func validateChannelRequest(req *struct {
	EntityID           string                 `json:"entity_id"`
	ProviderTier       string                 `json:"provider_tier"`
	ProviderCode       string                 `json:"provider_code"`
	ChannelCode        string                 `json:"channel_code"`
	CredentialVaultKey string                 `json:"credential_vault_key"`
	StrategyMode       string                 `json:"strategy_mode"`
	ProviderConfig     map[string]interface{} `json:"provider_config"`
	RoutingConfig      map[string]interface{} `json:"routing_config"`
}) error {
	if req.ProviderCode == "" {
		return apperr.ErrInvalidArgument.WithMessage("provider_code is required")
	}
	if req.CredentialVaultKey == "" {
		return apperr.ErrInvalidArgument.WithMessage("credential_vault_key is required")
	}
	if err := domain.ProviderTier(req.ProviderTier).Validate(); err != nil {
		return apperr.ErrInvalidArgument.WithMessage("invalid provider_tier")
	}
	if req.StrategyMode != "" && req.StrategyMode != "DEFAULT" && req.StrategyMode != "BURN_MODE" {
		return apperr.ErrInvalidArgument.WithMessage("invalid strategy_mode")
	}
	if req.ProviderConfig != nil {
		if _, err := json.Marshal(req.ProviderConfig); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("provider_config must be a valid JSON object")
		}
	}
	if req.RoutingConfig != nil {
		if _, err := json.Marshal(req.RoutingConfig); err != nil {
			return apperr.ErrInvalidArgument.WithMessage("routing_config must be a valid JSON object")
		}
	}
	return nil
}

func joinWithComma(items []string) string {
	if len(items) == 0 {
		return ""
	}
	result := items[0]
	for i := 1; i < len(items); i++ {
		result += ", " + items[i]
	}
	return result
}
