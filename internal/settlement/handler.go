package settlement

import (
	"log/slog"
	"net/http"

	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// Handler 暴露结算结转的内部触发入口（/internal/settlement/*）。
type Handler struct {
	svc *Service
	log *slog.Logger
}

func NewHandler(svc *Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// SettleAll 扫描所有待结算尝试并结转。内部接口，用于运维手动补跑与联调。
func (h *Handler) SettleAll(w http.ResponseWriter, r *http.Request) {
	results, err := h.svc.SettleAll(r.Context())
	if err != nil {
		h.log.Error("settlement: settle all failed", "err", err)
		httpx.ErrorResponse(w, http.StatusInternalServerError, "SETTLEMENT_FAILED", err.Error())
		return
	}
	var total int64
	var items int
	for _, res := range results {
		total += res.TotalMinor
		items += res.ItemCount
	}
	httpx.SuccessResponse(w, http.StatusOK, map[string]any{
		"batches":            results,
		"total_amount_minor": total,
		"item_count":         items,
	})
}
