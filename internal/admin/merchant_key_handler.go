package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// MerchantKeyHandler 管理商户 API 签名密钥（corporate_entity.sign_token_hash）。
//
// 与 MerchantAuth 的校验逻辑对应：DB 只存 SHA-256(hex)，签发时返回一次明文。
type MerchantKeyHandler struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func NewMerchantKeyHandler(pool *pgxpool.Pool, log *slog.Logger) *MerchantKeyHandler {
	return &MerchantKeyHandler{pool: pool, log: log}
}

func hashAPIKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Issue POST /admin/api/v3/merchants/{id}/api-keys
// 生成新密钥并替换旧密钥（旧密钥立即失效）。
func (h *MerchantKeyHandler) Issue(w http.ResponseWriter, r *http.Request) {
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "id must be a UUID")
		return
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		httpx.ErrorResponse(w, http.StatusInternalServerError, "KEY_GENERATION_FAILED", err.Error())
		return
	}
	token := "sk_" + hex.EncodeToString(buf)
	tokenHash := hashAPIKey(token)

	tag, err := h.pool.Exec(r.Context(), `
		UPDATE corporate_entity SET sign_token_hash = $2 WHERE id = $1
	`, merchantID, tokenHash)
	if err != nil {
		h.log.Error("merchant key: issue failed", "merchant_id", merchantID, "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "KEY_ISSUE_FAILED", err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.ErrorResponse(w, http.StatusNotFound, "MERCHANT_NOT_FOUND", "merchant not found")
		return
	}

	httpx.SuccessResponse(w, http.StatusOK, map[string]any{
		"merchant_id": merchantID,
		"api_key":     token,
		"issued_at":   time.Now().UTC().Format(time.RFC3339),
		"notice":      "api_key 只返回一次；签发后旧密钥立即失效",
	})
}

// Status GET /admin/api/v3/merchants/{id}/api-keys
// 只返回是否已配置与指纹，不返回明文或完整 hash。
func (h *MerchantKeyHandler) Status(w http.ResponseWriter, r *http.Request) {
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "id must be a UUID")
		return
	}
	var tokenHash *string
	err = h.pool.QueryRow(r.Context(), `
		SELECT sign_token_hash FROM corporate_entity WHERE id = $1
	`, merchantID).Scan(&tokenHash)
	if err != nil {
		httpx.ErrorResponse(w, http.StatusNotFound, "MERCHANT_NOT_FOUND", "merchant not found")
		return
	}
	configured := tokenHash != nil && *tokenHash != ""
	fingerprint := ""
	if configured {
		fingerprint = (*tokenHash)[:8]
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{
		"merchant_id": merchantID, "configured": configured, "fingerprint": fingerprint,
	})
}

// Revoke DELETE /admin/api/v3/merchants/{id}/api-keys
func (h *MerchantKeyHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "id must be a UUID")
		return
	}
	tag, err := h.pool.Exec(r.Context(), `
		UPDATE corporate_entity SET sign_token_hash = NULL WHERE id = $1
	`, merchantID)
	if err != nil {
		httpx.ErrorResponse(w, http.StatusInternalServerError, "KEY_REVOKE_FAILED", err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.ErrorResponse(w, http.StatusNotFound, "MERCHANT_NOT_FOUND", "merchant not found")
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"merchant_id": merchantID, "revoked": true})
}
