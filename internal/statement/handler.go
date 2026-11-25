package statement

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// Handler 对账单接口（admin + merchant）。
type Handler struct {
	svc *Service
	log *slog.Logger
}

func NewHandler(svc *Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func parseDay(v string) (time.Time, error) {
	if v == "" {
		return time.Now().AddDate(0, 0, -1).UTC().Truncate(24 * time.Hour), nil
	}
	return time.Parse("2006-01-02", v)
}

// AdminGenerate POST /admin/api/v3/statements/generate?date=YYYY-MM-DD
func (h *Handler) AdminGenerate(w http.ResponseWriter, r *http.Request) {
	day, err := parseDay(r.URL.Query().Get("date"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_DATE", "date must be YYYY-MM-DD")
		return
	}
	statements, err := h.svc.GenerateAll(r.Context(), day)
	if err != nil {
		h.log.Error("statement: generate all failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "STATEMENT_GENERATE_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{
		"business_date": day.Format("2006-01-02"),
		"count":         len(statements),
		"statements":    statements,
	})
}

// AdminList GET /admin/api/v3/statements?merchant_id=&date=
func (h *Handler) AdminList(w http.ResponseWriter, r *http.Request) {
	merchantID, err := uuid.Parse(r.URL.Query().Get("merchant_id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_MERCHANT_ID", "merchant_id must be a UUID")
		return
	}
	h.list(w, r, merchantID)
}

// MerchantList GET /merchant/statements?date=
func (h *Handler) MerchantList(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantFromContext(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusUnauthorized, "INVALID_MERCHANT", "merchant identity missing")
		return
	}
	h.list(w, r, merchantID)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, merchantID uuid.UUID) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	statements, err := h.svc.List(r.Context(), merchantID, r.URL.Query().Get("date"), limit)
	if err != nil {
		h.log.Error("statement: list failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "STATEMENT_LIST_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"statements": statements})
}

// MerchantGet GET /merchant/statements/{id}
func (h *Handler) MerchantGet(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantFromContext(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusUnauthorized, "INVALID_MERCHANT", "merchant identity missing")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_STATEMENT_ID", "id must be a UUID")
		return
	}
	st, err := h.svc.Get(r.Context(), merchantID, id)
	if err != nil {
		httpx.ErrorResponse(w, http.StatusInternalServerError, "STATEMENT_GET_FAILED", err.Error())
		return
	}
	if st == nil {
		httpx.ErrorResponse(w, http.StatusNotFound, "STATEMENT_NOT_FOUND", "statement not found")
		return
	}
	lines, err := h.svc.Lines(r.Context(), id)
	if err != nil {
		httpx.ErrorResponse(w, http.StatusInternalServerError, "STATEMENT_GET_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{"statement": st, "lines": lines})
}

// Download GET /merchant/statements/{id}/download —— 返回 CSV（并要求商户身份）。
func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantFromContext(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusUnauthorized, "INVALID_MERCHANT", "merchant identity missing")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_STATEMENT_ID", "id must be a UUID")
		return
	}
	h.writeCSV(w, r, merchantID, id)
}

// SignDownload GET /merchant/statements/{id}/download-link?ttl=1h
// 生成带时效的签名下载链接（P2）。
func (h *Handler) SignDownload(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantFromContext(r)
	if !ok {
		httpx.ErrorResponse(w, http.StatusUnauthorized, "INVALID_MERCHANT", "merchant identity missing")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_STATEMENT_ID", "id must be a UUID")
		return
	}

	ttl := time.Hour
	if v := r.URL.Query().Get("ttl"); v != "" {
		parsed, perr := time.ParseDuration(v)
		if perr != nil {
			httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_TTL", "ttl must be a duration, e.g. 30m or 24h")
			return
		}
		ttl = parsed
	}

	link, err := h.svc.SignDownload(r.Context(), merchantID, id, ttl)
	switch {
	case errors.Is(err, ErrSigningDisabled):
		httpx.ErrorResponse(w, http.StatusServiceUnavailable, "SIGNING_DISABLED", "statement signing key not configured")
		return
	case errors.Is(err, ErrNotFound):
		httpx.ErrorResponse(w, http.StatusNotFound, "STATEMENT_NOT_FOUND", "statement not found")
		return
	case err != nil:
		httpx.ErrorResponse(w, http.StatusInternalServerError, "STATEMENT_SIGN_FAILED", err.Error())
		return
	}
	httpx.SuccessResponse(w, http.StatusOK, link)
}

// PublicDownload GET /public/statements/{id}/download?expires=&signature=
// 使用签名链接下载，无需商户 JWT；签名绑定商户与对账单，且有过期时间。
func (h *Handler) PublicDownload(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_STATEMENT_ID", "id must be a UUID")
		return
	}
	expires, err := strconv.ParseInt(r.URL.Query().Get("expires"), 10, 64)
	if err != nil {
		httpx.ErrorResponse(w, http.StatusBadRequest, "INVALID_EXPIRES", "expires must be unix seconds")
		return
	}
	signature := r.URL.Query().Get("signature")

	merchantID, err := h.svc.VerifyDownload(r.Context(), id, expires, signature)
	switch {
	case errors.Is(err, ErrSigningDisabled):
		httpx.ErrorResponse(w, http.StatusServiceUnavailable, "SIGNING_DISABLED", "statement signing key not configured")
		return
	case errors.Is(err, ErrSignatureInvalid):
		httpx.ErrorResponse(w, http.StatusForbidden, "SIGNATURE_INVALID", "download signature invalid or expired")
		return
	case errors.Is(err, ErrNotFound):
		httpx.ErrorResponse(w, http.StatusNotFound, "STATEMENT_NOT_FOUND", "statement not found")
		return
	case err != nil:
		httpx.ErrorResponse(w, http.StatusInternalServerError, "STATEMENT_DOWNLOAD_FAILED", err.Error())
		return
	}
	h.writeCSV(w, r, merchantID, id)
}

func (h *Handler) writeCSV(w http.ResponseWriter, r *http.Request, merchantID, id uuid.UUID) {
	data, filename, err := h.svc.CSV(r.Context(), merchantID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.ErrorResponse(w, http.StatusNotFound, "STATEMENT_NOT_FOUND", "statement not found")
			return
		}
		httpx.ErrorResponse(w, http.StatusInternalServerError, "STATEMENT_DOWNLOAD_FAILED", err.Error())
		return
	}
	if sig := h.svc.SignBody(data); sig != "" {
		w.Header().Set("X-Statement-Signature", "hmac-sha256="+sig)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, bytes.NewReader(data))
}

func merchantFromContext(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(httpx.GetMerchantID(r.Context()))
	return id, err == nil
}
