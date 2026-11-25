package checkout

import (
	"context"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
)

// PAY-002: 商户回跳 URL allowlist（fail-closed）。
//
// success_url / cancel_url 必须同时满足：
//  1. HTTPS（isValidHTTPS 已在调用方校验，这里二次强制）；
//  2. 其 host 已登记在 merchant_return_url_allowlist（迁移 000053）。
//
// 未登记 = 拒绝创建（RETURN_URL_NOT_ALLOWED），防止 open redirect 与
// 支付结果页被伪造域收割。allowlist 管理接口在 Admin API（Phase 3）。
const errReturnURLNotAllowed = "RETURN_URL_NOT_ALLOWED"

// ensureReturnURLsAllowed 校验全部回跳 URL 的 host 都在该商户 allowlist 中。
func ensureReturnURLsAllowed(ctx context.Context, pool *pgxpool.Pool, merchantID uuid.UUID, urls ...string) error {
	hosts := make([]string, 0, len(urls))
	for _, raw := range urls {
		if !isValidHTTPS(raw) {
			return apperr.New(errReturnURLNotAllowed, 400)
		}
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return apperr.New(errReturnURLNotAllowed, 400)
		}
		hosts = append(hosts, strings.ToLower(u.Hostname()))
	}

	for _, host := range hosts {
		var allowed bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM merchant_return_url_allowlist
				WHERE merchant_id = $1 AND host = $2
			)
		`, merchantID, host).Scan(&allowed)
		if err != nil {
			return err
		}
		if !allowed {
			return apperr.New(errReturnURLNotAllowed, 400)
		}
	}
	return nil
}
