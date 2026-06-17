package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

type KillSwitchHandler struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
}

func NewKillSwitchHandler(pool *pgxpool.Pool, rdb *redis.Client) *KillSwitchHandler {
	return &KillSwitchHandler{pool: pool, rdb: rdb}
}

func (h *KillSwitchHandler) Lockdown(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Operator string `json:"operator"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("invalid request body"))
		return
	}
	if req.Operator == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("operator is required"))
		return
	}
	if req.Reason == "" {
		writeErr(w, apperr.ErrInvalidArgument.WithMessage("reason is required"))
		return
	}

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback(ctx)

	channelTag, err := tx.Exec(ctx, `
		UPDATE acquirer_channels
		SET status = 'DISABLED',
			version = version + 1,
			updated_at = now()
		WHERE status = 'ACTIVE'
	`)
	if err != nil {
		writeErr(w, err)
		return
	}

	groupTag, err := tx.Exec(ctx, `
		UPDATE pipeline_isolation_groups
		SET status = 'RISK_HOLD',
			frozen_reason = $1,
			frozen_by = $2,
			frozen_at = now(),
			updated_at = now()
		WHERE status IN ('ACTIVE', 'FROZEN')
	`, req.Reason, req.Operator)
	if err != nil {
		writeErr(w, err)
		return
	}

	auditValue, _ := json.Marshal(map[string]interface{}{
		"disabled_channels": channelTag.RowsAffected(),
		"risk_hold_groups":  groupTag.RowsAffected(),
	})
	_, err = tx.Exec(ctx, `
		INSERT INTO admin_override_audit_log (
			entity_type, entity_id, override_type, reason, operator, new_value, created_at
		) VALUES (
			'SYSTEM', gen_random_uuid(), 'SYSTEM_LOCKDOWN', $1, $2, $3::jsonb, now()
		)
	`, req.Reason, req.Operator, string(auditValue))
	if err != nil {
		writeErr(w, err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, err)
		return
	}

	payload := map[string]interface{}{
		"event":             "SYSTEM_LOCKDOWN",
		"operator":          req.Operator,
		"reason":            req.Reason,
		"disabled_channels": channelTag.RowsAffected(),
		"risk_hold_groups":  groupTag.RowsAffected(),
		"ts":                time.Now().UTC().Format(time.RFC3339),
	}

	if h.rdb != nil {
		body, _ := json.Marshal(payload)
		h.rdb.Publish(context.Background(), "system.fail_closed.lockdown", string(body))
		h.rdb.Publish(context.Background(), "channel.config.updated", "SYSTEM_LOCKDOWN")
		h.rdb.Publish(context.Background(), "config:acquirer_channel:reload", "SYSTEM_LOCKDOWN")
		h.rdb.Publish(context.Background(), "config:issuing_pool:reload", "SYSTEM_LOCKDOWN")
	}

	writeJSON(w, payload)
}

func (h *KillSwitchHandler) EventsStream(w http.ResponseWriter, r *http.Request) {
	if h.rdb == nil {
		writeErr(w, apperr.ErrInternal.WithMessage("redis is required for admin event stream"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, apperr.ErrInternal.WithMessage("streaming is not supported"))
		return
	}

	pubsub := h.rdb.Subscribe(r.Context(), "system.fail_closed.lockdown", "issuing.pool.fraud_tripped")
	defer pubsub.Close()

	for {
		msg, err := pubsub.ReceiveMessage(r.Context())
		if err != nil {
			return
		}

		eventName := "SYSTEM_LOCKDOWN"
		if msg.Channel == "issuing.pool.fraud_tripped" {
			eventName = "ISSUING_POOL_FRAUD_TRIPPED"
		}
		_, _ = w.Write([]byte("event: " + eventName + "\n"))
		_, _ = w.Write([]byte("data: " + msg.Payload + "\n\n"))
		flusher.Flush()
	}
}
