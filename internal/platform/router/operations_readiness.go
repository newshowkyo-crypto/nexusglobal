package router

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nexusglobal/mamr/internal/platform/config"
)

// Reports configuration evidence only, never a successful payment or connection.
func operationsReadiness(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checks := []map[string]interface{}{}
		add := func(name string, configured bool) {
			status := "需要设置"
			if configured {
				status = "已设置，待业务确认"
			}
			checks = append(checks, map[string]interface{}{"name": name, "configured": configured, "status": status})
		}
		add("正式运行环境", cfg.AppEnv == "production")
		add("支付机构连接", cfg.AcquirerMode == "real" && cfg.AcquirerProvider != "" && cfg.AcquirerProvider != "mock")
		add("银行与资金服务", cfg.ExternalMode == "real" && cfg.BankBaseURL != "" && cfg.BankAPIKey != "" && cfg.LPBaseURL != "" && cfg.LPAPIKey != "" && cfg.MAEBaseURL != "" && cfg.MAEAPIKey != "")
		add("账户安全保护", cfg.KMSMode == "real" && cfg.VaultBaseURL != "" && cfg.VaultToken != "")
		add("公网收款地址", strings.HasPrefix(cfg.CheckoutPublicBaseURL, "https://"))
		add("支付结果验证", len(cfg.WebhookSigningKey) >= 32)
		// The old HTTP order lookup is for non-production checkout. Live approvals
		// require separate evidence for each site's authenticated integration.
		checks = append(checks, map[string]interface{}{
			"name": "站点身份与授权", "configured": false, "status": "需完成站点密钥与授权验收",
		})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]interface{}{"checks": checks, "notice": "设置齐全后仍需完成机构连接、付款、退款和到账核对，才能确认可收款。"})
	}
}
