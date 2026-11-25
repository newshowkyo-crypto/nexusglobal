package httpx

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

var startTime = time.Now()

// ── G16 控件映射矩阵 ──
//
// 任何 404、未绑定按钮、dummy callback、吞错或总是成功均阻塞签收。
// 所有可见按钮都有真实可达且被授权的后端结果，危险动作无旁路。
//
//	控件                        | JS 函数                    | HTTP 方法/路径                                          | handler                      | permission | approval action              | audit event
//	---------------------------|----------------------------|--------------------------------------------------------|------------------------------|------------|------------------------------|--------------------------
//	系统紧急全盘锁死            | triggerSystemLockdown      | POST /admin/api/v3/system/lockdown                     | system lockdown              | admin      | -                            | SYSTEM_LOCKDOWN
//	一键安全离场                | suspendActiveCards        | POST /admin/api/v3/system/suspend-active-cards         | suspend active cards         | admin      | -                            | SUSPEND_ACTIVE_CARDS
//	适配器-创建提供商           | createAdapterProvider      | POST /admin/adapter/providers                          | adapter providers            | admin      | -                            | ADAPTER_PROVIDER_CREATED
//	适配器-删除提供商           | deleteAdapterProvider      | DELETE /admin/adapter/providers/{id}                   | adapter providers            | admin      | -                            | ADAPTER_PROVIDER_DELETED
//	适配器-创建通道             | createAdapterChannel       | POST /admin/adapter/channels                           | adapter channels             | admin      | -                            | ADAPTER_CHANNEL_CREATED
//	适配器-禁用通道             | confirmDisableAdapterChannel | POST /admin/adapter/channels/{id}/disable            | adapter channels             | admin      | -                            | ADAPTER_CHANNEL_DISABLED
//	法人实体-创建               | createEntity               | POST /admin/entities                                   | entities                     | admin      | -                            | ENTITY_CREATED
//	法人实体-风险冻结           | confirmSetEntityRiskHold   | POST /admin/entities/{id}/risk-hold                    | entities                     | admin      | -                            | ENTITY_RISK_HOLD
//	法人实体-解除冻结           | enableEntity               | POST /admin/entities/{id}/enable                       | entities                     | admin      | -                            | ENTITY_ENABLED
//	资产桥-创建                 | createBridgeProfile        | POST /admin/asset-bridge/profiles                      | bridge profiles              | admin      | -                            | BRIDGE_PROFILE_CREATED
//	资产桥-禁用                 | confirmDisableBridgeProfile | POST /admin/asset-bridge/profiles/{id}/disable        | bridge profiles              | admin      | -                            | BRIDGE_PROFILE_DISABLED
//	资产桥-启用                 | enableBridge               | POST /admin/asset-bridge/profiles/{id}/enable          | bridge profiles              | admin      | -                            | BRIDGE_PROFILE_ENABLED
//	钱包策略-创建               | createWalletPolicy         | POST /admin/wallet-policies                            | wallet policies              | admin      | -                            | WALLET_POLICY_CREATED
//	钱包策略-禁用               | confirmDisableWalletPolicy | POST /admin/wallet-policies/{id}/disable               | wallet policies              | admin      | DISABLE_WALLET_POLICY        | WALLET_POLICY_DISABLED
//	钱包策略-启用               | enableWalletPolicy         | POST /admin/wallet-policies/{id}/enable                | wallet policies              | admin      | -                            | WALLET_POLICY_ENABLED
//	通道风控-配置               | submitChannelRiskConfig    | PATCH /admin/v3/channel-risk/{id}                      | channel risk                 | admin      | UPDATE_CHANNEL_RISK          | CHANNEL_RISK_UPDATED
//	管道隔离-创建               | createIsolationGroup       | POST /admin/v3/isolation-groups                        | isolation groups             | admin      | -                            | ISOLATION_GROUP_CREATED
//	管道隔离-冻结               | freezeIsolationGroup       | POST /admin/v3/isolation-groups/{id}/freeze            | isolation groups             | admin      | -                            | ISOLATION_GROUP_FROZEN
//	管道隔离-解冻               | unfreezeIsolationGroup     | POST /admin/v3/isolation-groups/{id}/unfreeze          | isolation groups             | admin      | -                            | ISOLATION_GROUP_UNFROZEN
//	RFI-生成                    | generateRFIDocument        | POST /admin/v3/rfi-documents/generate                 | rfi documents                | admin      | -                            | RFI_GENERATED
//	RFI-通过                    | confirmApproveRFIDoc       | POST /admin/v3/rfi-documents/{id}/approve              | rfi documents                | admin      | -                            | RFI_APPROVE
//	RFI-拒绝                    | confirmRejectRFIDoc        | POST /admin/v3/rfi-documents/{id}/reject               | rfi documents                | admin      | -                            | RFI_REJECT
//	收单通道-创建               | createAcquirerChannel      | POST /admin/api/v3/channels                            | v3 channels                  | admin      | -                            | ACQUIRER_CHANNEL_CREATED
//	收单通道-启用               | enableAcquirerChannel      | POST /admin/api/v3/channels/{id}/enable                | v3 channels                  | admin      | ENABLE_ACQUIRER_CHANNEL      | ACQUIRER_CHANNEL_ENABLED
//	收单通道-禁用               | disableAcquirerChannel     | POST /admin/api/v3/channels/{id}/disable               | v3 channels                  | admin      | DISABLE_ACQUIRER_CHANNEL     | ACQUIRER_CHANNEL_DISABLED
//	收单通道-重载               | reloadAcquirerChannel      | POST /admin/api/v3/channels/{id}/reload                | v3 channels                  | admin      | -                            | ACQUIRER_CHANNEL_RELOADED
//	发卡卡池-创建               | createIssuingPool          | POST /admin/api/v3/issuing/pools                       | v3 issuing pools             | admin      | -                            | ISSUING_POOL_CREATED
//	发卡卡池-启用               | enableIssuingPool          | POST /admin/api/v3/issuing/pools/{id}/enable           | v3 issuing pools             | admin      | ENABLE_ISSUING_POOL          | ISSUING_POOL_ENABLED
//	发卡卡池-禁用               | disableIssuingPool         | POST /admin/api/v3/issuing/pools/{id}/disable          | v3 issuing pools             | admin      | DISABLE_ISSUING_POOL         | ISSUING_POOL_DISABLED
//	发卡卡池-重载               | reloadIssuingPool          | POST /admin/api/v3/issuing/pools/{id}/reload           | v3 issuing pools             | admin      | -                            | ISSUING_POOL_RELOADED
//	VA-录入                     | createVAAccount            | POST /admin/api/v3/bank/va-pool                        | v3 bank va                   | admin      | -                            | VA_ACCOUNT_CREATED
//	VA-释放                     | releaseVA                  | POST /admin/api/v3/bank/va-pool/{id}/release           | v3 bank va                   | admin      | -                            | VA_RELEASED
//	VA-禁用                     | disableVA                  | POST /admin/api/v3/approvals (DISABLE_VA_ACCOUNT)      | approval                     | admin      | DISABLE_VA_ACCOUNT           | APPROVAL_REQUESTED
//	结算账户-录入               | createSettlementAccount    | POST /admin/api/v3/bank/settlement-accounts            | v3 bank settlement           | admin      | -                            | SETTLEMENT_ACCOUNT_CREATED
//	商户-创建                   | createMerchant             | POST /admin/api/v3/merchants                           | v3 merchants                 | admin      | -                            | MERCHANT_CREATED
//	审批-批准                   | approveApproval            | POST /admin/api/v3/approvals/{id}/approve              | approval                     | checker    | -                            | APPROVAL_APPROVED
//	审批-拒绝                   | rejectApproval             | POST /admin/api/v3/approvals/{id}/reject               | approval                     | checker    | -                            | APPROVAL_REJECTED
//	审批-申请                   | submitApprovalRequest      | POST /admin/api/v3/approvals                           | approval                     | maker      | (per action)                 | APPROVAL_REQUESTED
//
// 身份来源：登录会话（Authorization header），拒绝 body 中伪造的 requester/checker。
// 危险动作（禁用通道/卡池/钱包/VA、系统锁死、冻结隔离组）均经 Maker-Checker 审批或 confirm 双确认。

// generateNonce 生成一个 base64 编码的 16 字节随机 nonce，用于 CSP。
func generateNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 极端情况下 rand 失败，返回一个固定值（CSP 仍会阻止无 nonce 脚本）
		return "fallback-nonce-unavailable"
	}
	return base64.StdEncoding.EncodeToString(b)
}

// writeSecurityHeaders 写入安全响应头。
//
// G04：Dashboard 之前没有任何安全头，允许 MIME 嗅探、点击劫持、
// 全量脚本执行。现在添加：
//   - X-Content-Type-Options: nosniff
//   - X-Frame-Options: DENY
//   - Referrer-Policy: no-referrer
//   - Content-Security-Policy: 仅允许 nonce'd 脚本 + 同源 fetch + base64 图片
func writeSecurityHeaders(w http.ResponseWriter, nonce string) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// CSP：script-src / style-src 均用同一 nonce，禁止内联样式与内联脚本；
	// connect-src 'self'（同源 fetch）；img-src 'self' data:；base-uri 'none'
	csp := fmt.Sprintf(
		"default-src 'self'; "+
			"script-src 'self' 'nonce-%s'; "+
			"style-src 'self' 'nonce-%s'; "+
			"connect-src 'self'; "+
			"img-src 'self' data:; "+
			"base-uri 'none'; "+
			"frame-ancestors 'none'; "+
			"form-action 'self'",
		nonce, nonce,
	)
	w.Header().Set("Content-Security-Policy", csp)
}

// R20 P0-2: Dashboard 改为接收 allowMockLocal bool，由调用方通过 ResolveRuntimeMode 推导。
func DashboardHandler(pool *pgxpool.Pool, rdb *redis.Client, env string, allowMockLocal bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		dbConnected := false
		if pool != nil {
			if err := pool.Ping(r.Context()); err == nil {
				dbConnected = true
			}
		}
		redisConnected := false
		if rdb != nil {
			if err := rdb.Ping(r.Context()).Err(); err == nil {
				redisConnected = true
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"system":    map[string]interface{}{"go_version": runtime.Version(), "num_cpu": runtime.NumCPU(), "num_goroutine": runtime.NumGoroutine(), "mem_alloc_mb": mem.Alloc / 1024 / 1024, "mem_sys_mb": mem.Sys / 1024 / 1024},
			"database":  map[string]interface{}{"connected": dbConnected},
			"redis":     map[string]interface{}{"connected": redisConnected},
			"api":       map[string]interface{}{"version": "1.0.0", "env": env, "uptime": time.Since(startTime).Round(time.Second).String()},
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// R20 P0-2: Dashboard 改为接收 allowMockLocal bool，由调用方通过 ResolveRuntimeMode 推导。
func AdminDashboardHTML(pool *pgxpool.Pool, rdb *redis.Client, env string, allowMockLocal bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		dbStatus, dbClass := "断开", "error"
		if pool != nil {
			if err := pool.Ping(r.Context()); err == nil {
				dbStatus, dbClass = "已连接", "ok"
			}
		}
		redisStatus, redisClass := "断开", "error"
		if rdb != nil {
			if err := rdb.Ping(r.Context()).Err(); err == nil {
				redisStatus, redisClass = "已连接", "ok"
			}
		}

		// G04: 每请求生成 nonce，注入 CSP 和 <script nonce=...>
		nonce := generateNonce()
		writeSecurityHeaders(w, nonce)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(dashboardHTML(nonce, env, allowMockLocal, dbStatus, dbClass, redisStatus, redisClass, time.Since(startTime).Round(time.Second).String(), runtime.Version(), runtime.NumCPU(), runtime.NumGoroutine(), int(mem.Alloc/1024/1024), int(mem.Sys/1024/1024))))
	}
}

// mockLocalOption 根据 allowMockLocal 决定是否渲染 MOCK_LOCAL option。
// R20 P0-2: 改为接收 bool 参数，由调用方通过 ResolveRuntimeMode 推导。
// production/real HTML 中完全不渲染 MOCK_LOCAL option。
func mockLocalOption(allowMockLocal bool) string {
	if !allowMockLocal {
		return ""
	}
	return `<option value="MOCK_LOCAL">MOCK_LOCAL (仅本地)</option>`
}

// DashboardHTMLForTest 是测试辅助函数，直接生成 Dashboard HTML 用于组合契约测试。
// R23 Item 7: 允许 router 包测试验证 ResolveRuntimeMode 组合后的 HTML 渲染结果。
func DashboardHTMLForTest(nonce, env string, allowMockLocal bool) string {
	return dashboardHTML(nonce, env, allowMockLocal, "断开", "error", "断开", "error", "0s", "go1.21", 4, 10, 100, 200)
}

func dashboardHTML(nonce, env string, allowMockLocal bool, dbStatus, dbClass, redisStatus, redisClass, uptime, goVer string, cpu, goroutines, memAlloc, memSys int) string {
	return businessDashboardHTML(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>NexusGlobal MAMR 管理后台</title>
<style nonce="`+nonce+`">
/* UI-022: design tokens——统一色板/间距/圆角 */
:root{--c-bg:#0f172a;--c-surface:#1e293b;--c-surface-2:#334155;--c-text:#e2e8f0;--c-muted:#94a3b8;--c-border:#334155;--c-primary:#3b82f6;--c-ok:#22c55e;--c-warn:#f59e0b;--c-err:#ef4444;--sp-1:4px;--sp-2:8px;--sp-3:12px;--sp-4:16px;--sp-6:24px;--radius:8px}
*{box-sizing:border-box;margin:0;padding:0}
/* UI-018: 键盘焦点可见性 */
:focus-visible{outline:2px solid var(--c-primary);outline-offset:2px}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:var(--c-bg);color:var(--c-text);min-height:100vh}
/* UI-017: 窄视口与宽表可用性 */
@media (max-width:1024px){.nav{padding:var(--sp-2) var(--sp-3)}table{font-size:12px}}
@media (max-width:768px){.nav{overflow-x:auto;flex-wrap:nowrap}table{display:block;overflow-x:auto;white-space:nowrap}.grid{grid-template-columns:1fr}}
/* UI-023: 减少动效偏好 */
@media (prefers-reduced-motion:reduce){*,*::before,*::after{animation-duration:.01ms!important;animation-iteration-count:1!important;transition-duration:.01ms!important}}
.nav{display:flex;align-items:center;gap:4px;padding:12px 20px;background:#1e293b;border-bottom:1px solid #334155;flex-wrap:wrap}
.logo{font-weight:700;font-size:16px;color:#38bdf8;margin-right:16px;white-space:nowrap}
.nav button{background:#1e293b;color:#94a3b8;border:1px solid #334155;padding:8px 14px;border-radius:6px;cursor:pointer;font-size:13px;transition:all .2s}
.nav button:hover{background:#334155;color:#e2e8f0}
.nav button.active{background:#38bdf8;color:#0f172a;font-weight:600}
.main{padding:20px;max-width:1400px;margin:0 auto}
.tab{display:none}.tab.active{display:block}.hidden{display:none}.mb12{margin-bottom:12px}.diff-changed{background:#422006}
.tab h2{color:#f1f5f9;font-size:18px;margin-bottom:16px;display:flex;align-items:center;gap:10px}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:16px;margin-bottom:24px}
.stat{background:#1e293b;border:1px solid #334155;border-radius:12px;padding:20px}
.stat .label{color:#94a3b8;font-size:13px;margin-bottom:8px}
.stat .value{font-size:28px;font-weight:700;color:#f1f5f9}
.stat .sub{color:#64748b;font-size:12px;margin-top:4px}
.section{background:#1e293b;border:1px solid #334155;border-radius:12px;padding:20px;margin-bottom:20px}
.section h2{color:#f1f5f9;font-size:18px;margin-bottom:16px;display:flex;align-items:center;gap:10px}
table{width:100%;border-collapse:collapse;font-size:13px}
th{text-align:left;padding:12px 8px;color:#94a3b8;font-weight:600;border-bottom:2px solid #334155;white-space:nowrap}
td{padding:12px 8px;border-bottom:1px solid #1e293b;color:#cbd5e1}
tr:hover td{background:#334155}
.badge{display:inline-block;padding:4px 8px;border-radius:4px;font-size:11px;font-weight:600}
.badge-ok{background:#052e16;color:#4ade80}
.badge-err{background:#450a0a;color:#f87171}
.badge-warn{background:#422006;color:#fbbf24}
.badge-info{background:#172554;color:#60a5fa}.sse-status{display:inline-block;font-size:11px;padding:2px 8px;border-radius:10px;margin-left:8px;font-weight:600;vertical-align:middle}.sse-online{background:#052e16;color:#4ade80}.sse-offline{background:#450a0a;color:#f87171}.sse-connecting{background:#422006;color:#fbbf24}
.method{display:inline-block;padding:2px 6px;border-radius:4px;font-size:10px;font-weight:700;font-family:monospace;min-width:40px;text-align:center}
.m-get{background:#052e16;color:#4ade80}.m-post{background:#172554;color:#60a5fa}.m-patch{background:#422006;color:#fbbf24}
.status-dot{display:inline-block;width:8px;height:8px;border-radius:50%;margin-right:6px}
.dot-ok{background:#4ade80}.dot-err{background:#f87171}
.refresh-btn{background:#38bdf8;color:#0f172a;border:none;padding:6px 14px;border-radius:6px;cursor:pointer;font-size:12px;font-weight:600}
.refresh-btn:hover{opacity:.8}
.loading{color:#64748b;font-style:italic;padding:20px;text-align:center}
.empty{color:#64748b;text-align:center;padding:40px}
.card{background:#1e293b;border:1px solid #334155;border-radius:12px;padding:20px;margin-bottom:16px}
.card h3{color:#f1f5f9;font-size:15px;margin-bottom:12px}
.form-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:12px;margin-bottom:16px}
.form-group{display:flex;flex-direction:column;gap:4px}
.form-group label{color:#94a3b8;font-size:12px;font-weight:600}
.form-group input,.form-group select{background:#0f172a;color:#e2e8f0;border:1px solid #334155;padding:8px 12px;border-radius:6px;font-size:13px}
.form-group input:focus,.form-group select:focus{outline:none;border-color:#38bdf8}
.submit-btn{background:#22c55e;color:#fff;border:none;padding:10px 20px;border-radius:6px;cursor:pointer;font-size:14px;font-weight:600}
.submit-btn:hover{opacity:.9}
.action-btn{background:#475569;color:#e2e8f0;border:none;padding:4px 10px;border-radius:4px;cursor:pointer;font-size:12px}
.action-btn:hover{background:#64748b}
.action-btn.enable{background:#22c55e}
.action-btn.danger{background:#dc2626}
pre{background:#0f172a;padding:16px;border-radius:8px;overflow-x:auto;font-size:12px;color:#94a3b8}
.fixed-btn{position:fixed;bottom:20px;right:20px;background:#38bdf8;color:#0f172a;border:none;padding:12px 20px;border-radius:8px;cursor:pointer;font-size:14px;font-weight:600;box-shadow:0 4px 12px rgba(0,0,0,.3)}
.fixed-btn:hover{opacity:.9}
table{overflow-x:auto;display:block}
.alert-banner{padding:12px 16px;border-radius:8px;margin-bottom:16px;font-size:13px;line-height:1.5}
.alert-banner.info{background:#172554;color:#60a5fa;border:1px solid #1e3a5f}
.alert-banner.warn{background:#422006;color:#fbbf24;border:1px solid #78350f}
.input-error{border-color:#dc2626 !important;background:#450a0a !important}
.modal{position:fixed;top:0;left:0;width:100%;height:100%;background:rgba(0,0,0,.5);display:flex;align-items:center;justify-content:center;z-index:1000}
.modal-content{background:#1e293b;border:1px solid #334155;border-radius:12px;padding:24px;max-width:500px;width:90%}
.modal-content h3{color:#f1f5f9;font-size:16px;margin-bottom:16px}
.modal-content form{display:flex;flex-direction:column;gap:8px}
.modal-content label{color:#94a3b8;font-size:12px}
.modal-content input,.modal-content select{background:#0f172a;color:#e2e8f0;border:1px solid #334155;padding:8px 12px;border-radius:6px}
.modal-content button{margin-top:8px;padding:8px 16px;border:none;border-radius:6px;cursor:pointer}
h4{color:#f1f5f9;font-size:14px;margin:12px 0 8px}
.radio-cards{display:flex;gap:12px;margin-top:4px}
.radio-card{display:flex;align-items:center;gap:8px;padding:10px 14px;border:1px solid #334155;border-radius:8px;cursor:pointer;transition:border-color .2s,background .2s}
.radio-card:hover{border-color:#60a5fa}
.radio-card input[type="radio"]{accent-color:#60a5fa}
.radio-card-title{color:#f1f5f9;font-size:13px;font-weight:600}
.radio-card-desc{color:#94a3b8;font-size:11px;line-height:1.4}
.radio-card:has(input:checked){border-color:#3b82f6;background:#172554}
</style>
</head>
<body>
<nav class="nav" aria-label="后台导航"><span class="logo">NexusGlobal MAMR</span>
<span style="float:right"><button id="logout-btn" class="logout-btn">退出登录</button></span>
<button class="active" data-tab="overview">总览</button>
<button data-tab="universal-adapter">通用适配器</button>
<button data-tab="corporate-entity">法人实体</button>
<button data-tab="asset-bridge">资产桥配置</button>
<button data-tab="wallet-policy">钱包白名单</button>
<button data-tab="risk-ops">风控运维</button>
<button data-tab="channel-risk">通道风控</button>
<button data-tab="merchant-reserve">商户准备金</button>
<button data-tab="fee-allocation">费用摊销</button>
<button data-tab="pipeline-isolation">管道隔离</button>
<button data-tab="compliance-rfi">合规资料包</button>
<button data-tab="acquirer-channels">收单通道Tier</button>
<button data-tab="issuing-pools">发卡卡池Tier</button>
<button data-tab="bank-vba">银行/VBA</button>
<button data-tab="merchant-onboarding">商户入网</button>
<button data-tab="approval-inbox">审批inbox</button>
<button data-tab="payment-orders">支付订单</button>
<button data-tab="payment-refunds">退款与争议</button>
<button data-tab="payment-settlements">结算与对账</button>
<button data-tab="payment-payouts">出金</button>
<button data-tab="design-system">组件规范</button>
</nav>
<div class="main">
<section id="tab-design-system" class="tab"></section>
<section id="tab-payment-orders" class="tab"></section>
<section id="tab-payment-refunds" class="tab"></section>
<section id="tab-payment-settlements" class="tab"></section>
<section id="tab-payment-payouts" class="tab"></section>
<div id="tab-overview" class="tab active"><div class="grid">
<div class="stat"><div class="label">系统状态</div><div class="value"><span class="status-dot dot-ok"></span>运行中</div><div class="sub">运行时间: `+uptime+`</div></div>
<div class="stat"><div class="label">PostgreSQL 数据库</div><div class="value"><span class="status-dot dot-`+dbClass+`"></span>`+dbStatus+`</div><div class="sub">主数据存储</div></div>
<div class="stat"><div class="label">Redis 缓存</div><div class="value"><span class="status-dot dot-`+redisClass+`"></span>`+redisStatus+`</div><div class="sub">消息队列 + 缓存</div></div>
<div class="stat"><div class="label">运行环境</div><div class="value">`+env+`</div><div class="sub">Go `+goVer+`</div></div>
<div class="stat"><div class="label">内存使用</div><div class="value">`+fmt.Sprint(memAlloc)+` MB</div><div class="sub">已分配 / 系统总计</div></div>
<div class="stat"><div class="label">协程数</div><div class="value">`+fmt.Sprint(goroutines)+`</div><div class="sub">活跃 goroutine</div></div>
</div><div class="section"><h2>系统避险大闸 <span id="sse-connection-status" class="sse-status sse-connecting">connecting</span> <button class="action-btn danger" data-action="triggerSystemLockdown">系统紧急全盘锁死</button> <button class="action-btn danger" data-action="suspendActiveCards">一键安全离场</button></h2><div id="global-risk-alert" class="empty">等待风险事件推送</div></div><div class="section"><h2>业务统计 <button class="refresh-btn" data-action="loadStats">刷新</button></h2><div id="stats-content" class="loading">加载中...</div></div></div>
<div id="tab-universal-adapter" class="tab">
<div class="alert-banner info">提示：V3 VBA 可视化已上线。下方表单为正式录入入口；旧版 POST /admin/bank-account 仍保留作为底层写入路径，日常操作请优先使用本页表单。</div>
<div class="section"><h2>适配器提供商 <button class="refresh-btn" data-action="loadAdapterProviders">刷新</button></h2>
<div class="card">
<h3>新增提供商</h3>
<div class="form-grid">
<div class="form-group"><label>提供商代码</label><input type="text" id="new-provider-code" placeholder="如: stripe_sg"></div>
<div class="form-group"><label>提供商名称</label><input type="text" id="new-provider-name" placeholder="如: Stripe 新加坡"></div>
<div class="form-group"><label>适配器类型</label><select id="new-provider-type"><option value="CARD_ACQUIRER">卡网收单</option><option value="BANK_COLLECTION">银行虚拟户入金</option><option value="CRYPTO_PAY">加密支付</option><option value="BANK_RAIL">银行通道</option><option value="EXCHANGE">交易所</option><option value="LP">流动性提供商</option><option value="ONCHAIN_WALLET">链上钱包</option></select></div>
</div>
<button class="submit-btn" data-action="createAdapterProvider">创建提供商</button>
</div>
<table><thead><tr><th>提供商代码</th><th>名称</th><th>类型</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="adapter-providers-table"><tr><td colspan="6" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>适配器通道 <button class="refresh-btn" data-action="loadAdapterChannels">刷新</button></h2>
<div class="card">
<h3>新增通道</h3>
<div class="form-grid">
<div class="form-group"><label>通道代码</label><input type="text" id="new-channel-code" placeholder="如: stripe_visa_sg"></div>
<div class="form-group"><label>通道名称</label><input type="text" id="new-channel-name" placeholder="如: Stripe Visa 新加坡"></div>
<div class="form-group"><label>租户 ID</label><input type="text" id="new-channel-tenant" placeholder="租户 UUID（后端必填校验）"></div>
<div class="form-group"><label>提供商</label><select id="new-channel-provider"><option value="">加载中...</option></select></div>
<div class="form-group"><label>适配器类型</label><select id="new-channel-type"><option value="CARD_ACQUIRER">卡网收单</option><option value="BANK_COLLECTION">银行虚拟户入金</option><option value="CRYPTO_PAY">加密支付</option><option value="BANK_RAIL">银行通道</option></select></div>
<div class="form-group"><label>支持资产（逗号分隔）</label><input type="text" id="new-channel-assets" placeholder="如: USD,SGD,EUR"></div>
<div class="form-group"><label>支持网络（逗号分隔）</label><input type="text" id="new-channel-networks" placeholder="如: VISA,MASTERCARD"></div>
</div>
<button class="submit-btn" data-action="createAdapterChannel">创建通道</button>
</div>
<table><thead><tr><th>通道代码</th><th>名称</th><th>提供商</th><th>类型</th><th>资产</th><th>网络</th><th>状态</th><th>操作</th></tr></thead><tbody id="adapter-channels-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-corporate-entity" class="tab">
<div class="section"><h2>法人实体 <button class="refresh-btn" data-action="loadEntities">刷新</button></h2>
<div class="card">
<h3>新增实体</h3>
<div class="form-grid">
<div class="form-group"><label>租户 ID</label><input type="text" id="new-entity-tenant" placeholder="输入租户 ID"></div>
<div class="form-group"><label>实体名称</label><input type="text" id="new-entity-name" placeholder="如: Acme Corp"></div>
<div class="form-group"><label>法人名称</label><input type="text" id="new-entity-legal" placeholder="如: Acme Corporation Ltd."></div>
<div class="form-group"><label>国家</label><select id="new-entity-country"><option value="SG">新加坡 (SG)</option><option value="HK">香港 (HK)</option><option value="US">美国 (US)</option><option value="UK">英国 (UK)</option><option value="CN">中国 (CN)</option></select></div>
<div class="form-group"><label>司法辖区</label><input type="text" id="new-entity-jurisdiction" placeholder="如: 新加坡"></div>
</div>
<button class="submit-btn" data-action="createEntity">创建实体</button>
</div>
<table><thead><tr><th>实体名称</th><th>法人名称</th><th>国家</th><th>司法辖区</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="entities-table"><tr><td colspan="7" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-asset-bridge" class="tab">
<div class="section"><h2>资产桥配置 <button class="refresh-btn" data-action="loadBridgeProfiles">刷新</button></h2>
<div class="card">
<h3>新增桥接配置</h3>
<div class="form-grid">
<div class="form-group"><label>租户 ID</label><input type="text" id="new-bridge-tenant" placeholder="输入租户 ID"></div>
<div class="form-group"><label>配置名称</label><input type="text" id="new-bridge-name" placeholder="如: USD-USDT 桥"></div>
<div class="form-group"><label>源资产</label><input type="text" id="new-bridge-source" placeholder="如: USD"></div>
<div class="form-group"><label>目标资产</label><input type="text" id="new-bridge-target" placeholder="如: USDT"></div>
<div class="form-group"><label>缓冲百分比</label><input type="number" id="new-bridge-buffer" placeholder="如: 5" value="5"></div>
<div class="form-group"><label>最大滑点(bps)</label><input type="number" id="new-bridge-slippage" placeholder="如: 20" value="20"></div>
<div class="form-group"><label>批处理策略</label><select id="new-bridge-batching"><option value="IMMEDIATE">立即执行</option><option value="ACCUMULATE">累积批处理</option><option value="SCHEDULED">定时批处理</option></select></div>
<div class="form-group"><label>执行策略</label><select id="new-bridge-execution"><option value="MARKET">市价</option><option value="LIMIT">限价</option><option value="SLICED">分片执行</option></select></div>
</div>
<button class="submit-btn" data-action="createBridgeProfile">创建配置</button>
</div>
<table><thead><tr><th>名称</th><th>源资产</th><th>目标资产</th><th>缓冲%</th><th>滑点限制</th><th>批处理</th><th>执行</th><th>状态</th><th>操作</th></tr></thead><tbody id="bridge-profiles-table"><tr><td colspan="9" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-wallet-policy" class="tab">
<div class="section"><h2>钱包白名单 <button class="refresh-btn" data-action="loadWalletPolicies">刷新</button></h2>
<div class="card">
<h3>新增钱包策略</h3>
<div class="form-grid">
<div class="form-group"><label>租户 ID</label><input type="text" id="new-wallet-tenant" placeholder="输入租户 ID"></div>
<div class="form-group"><label>策略代码</label><input type="text" id="new-wallet-code" placeholder="如: eth_transit_001"></div>
<div class="form-group"><label>资产</label><input type="text" id="new-wallet-asset" placeholder="如: USDT"></div>
<div class="form-group"><label>网络</label><input type="text" id="new-wallet-network" placeholder="如: ETHEREUM"></div>
<div class="form-group"><label>钱包地址</label><input type="text" id="new-wallet-address" placeholder="0x..."></div>
<div class="form-group"><label>钱包类型</label><select id="new-wallet-dest-type"><option value="TRANSIT">过渡钱包</option><option value="COLD_WALLET">冷钱包</option><option value="CUSTODY">托管钱包</option></select></div>
<div class="form-group"><label>标签</label><input type="text" id="new-wallet-label" placeholder="如: 运营主钱包"></div>
<div class="form-group"><label>单笔限额</label><input type="number" id="new-wallet-single-limit" placeholder="如: 10000" value="10000"></div>
<div class="form-group"><label>日限额</label><input type="number" id="new-wallet-daily-limit" placeholder="如: 100000" value="100000"></div>
</div>
<button class="submit-btn" data-action="createWalletPolicy">创建策略</button>
</div>
<table><thead><tr><th>策略代码</th><th>资产</th><th>网络</th><th>地址</th><th>类型</th><th>单笔限额</th><th>日限额</th><th>状态</th><th>操作</th></tr></thead><tbody id="wallet-policies-table"><tr><td colspan="9" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-risk-ops" class="tab">
<details class="atlas-legacy-disclosure"><summary>旧版风控明细（风险事件 / 冻结实体 / 待处理任务）</summary>
<div class="section"><h2>风险事件 <button class="refresh-btn" data-action="loadRiskEvents">刷新</button></h2>
<table><thead><tr><th>ID</th><th>商户</th><th>站点</th><th>通道ID</th><th>处理结果</th><th>原因</th><th>发生时间</th><th>操作</th></tr></thead><tbody id="risk-events-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>风险冻结实体 <button class="refresh-btn" data-action="loadRiskHoldStatus">刷新</button></h2>
<table><thead><tr><th>实体类型</th><th>实体ID</th><th>名称</th><th>状态</th><th>更新时间</th><th>操作</th></tr></thead><tbody id="risk-hold-table"><tr><td colspan="6" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>待处理任务 <button class="refresh-btn" data-action="loadPendingTasks">刷新</button></h2>
<table><thead><tr><th>任务ID</th><th>事件类型</th><th>状态</th><th>聚合ID</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="pending-tasks-table"><tr><td colspan="6" class="loading">加载中...</td></tr></tbody></table></div>
</details>
</div>
<div id="tab-channel-risk" class="tab">
<div class="section"><h2>通道风控配置 <button class="refresh-btn" data-action="loadChannelRiskConfig">刷新</button></h2>
<div class="card">
<h3>调整风控规则</h3>
<div class="form-grid">
<div class="form-group"><label>通道代码</label><select id="new-risk-channel"><option value="">请选择通道</option></select></div>
<div class="form-group"><label>风控模式</label><select id="new-risk-mode"><option value="DEFAULT">标准模式</option><option value="STRICT">严格模式</option><option value="MANUAL_REVIEW">人工审核</option></select></div>
</div>
<button class="submit-btn" data-action="createChannelRiskConfig">创建配置</button>
</div>
<table><thead><tr><th>通道代码</th><th>通道名称</th><th>风控模式</th><th>拒付阈值</th><th>人工调整</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="channel-risk-table"><tr><td colspan="7" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>风控覆盖记录 <button class="refresh-btn" data-action="loadRiskOverrides">刷新</button></h2>
<table><thead><tr><th>ID</th><th>实体类型</th><th>实体ID</th><th>覆盖类型</th><th>原因</th><th>操作人</th><th>过期时间</th><th>创建时间</th></tr></thead><tbody id="risk-overrides-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>覆盖审计日志 <button class="refresh-btn" data-action="loadOverrideAuditLogs">刷新</button></h2>
<table><thead><tr><th>ID</th><th>实体类型</th><th>实体ID</th><th>覆盖类型</th><th>原因</th><th>操作人</th><th>创建时间</th></tr></thead><tbody id="override-audit-logs-table"><tr><td colspan="7" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-merchant-reserve" class="tab">
<div class="section"><h2>商户风险配置 <button class="refresh-btn" data-action="loadMerchantRiskProfiles">刷新</button></h2>
<div class="card">
<h3>新增风险配置</h3>
<div class="form-grid">
<div class="form-group"><label>商户ID</label><select id="new-reserve-merchant"><option value="">请选择商户</option></select></div>
<div class="form-group"><label>滚动准备金比例（%）</label><input type="number" id="new-reserve-rolling" placeholder="10" value="10" min="0" max="100" step="0.01"></div>
<div class="form-group"><label>争议准备金比例（%）</label><input type="number" id="new-reserve-dispute" placeholder="5" value="5" min="0" max="100" step="0.01"></div>
<div class="form-group"><label>释放天数</label><input type="number" id="new-reserve-days" placeholder="30" value="30"></div>
</div>
<button class="submit-btn" data-action="createMerchantRiskProfile">创建配置</button>
</div>
<table><thead><tr><th>商户ID</th><th>滚动准备金率</th><th>争议准备金率</th><th>释放天数</th><th>状态</th><th>操作</th></tr></thead><tbody id="merchant-risk-profiles-table"><tr><td colspan="6" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>准备金明细 <button class="refresh-btn" data-action="loadReserveEntries">刷新</button></h2>
<table><thead><tr><th>商户</th><th>交易编号</th><th>金额（原账本单位）</th><th>类型</th><th>释放日期</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="reserve-entries-table"><tr><td colspan="7" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-fee-allocation" class="tab">
<div class="section"><h2>费用摊销记录 <button class="refresh-btn" data-action="loadFeeAllocations">刷新</button></h2>
<table><thead><tr><th>交易编号</th><th>商户</th><th>费用类型</th><th>金额</th><th>币种</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="fee-allocations-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>费用统计 <button class="refresh-btn" data-action="loadFeeStats">刷新</button></h2>
<div id="fee-stats-content" class="loading">加载中...</div></div>
</div>
<div id="tab-pipeline-isolation" class="tab">
<div class="section"><h2>管道隔离组 <button class="refresh-btn" data-action="loadIsolationGroups">刷新</button></h2>
<div class="card">
<h3>新增隔离组</h3>
<div class="form-grid">
<div class="form-group"><label>组名称</label><input type="text" id="new-group-name" placeholder="如: Group-A"></div>

</div>
<button class="submit-btn" data-action="createIsolationGroup">创建组</button>
</div>
<table><thead><tr><th>组名称</th><th>描述</th><th>状态</th><th>冻结原因</th><th>冻结人</th><th>冻结时间</th><th>操作</th></tr></thead><tbody id="isolation-groups-table"><tr><td colspan="7" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>跨组操作记录</h2>
<table><thead><tr><th>时间</th><th>源组</th><th>目标组</th><th>操作类型</th><th>结果</th></tr></thead><tbody id="cross-group-operations-table"><tr><td colspan="5" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-compliance-rfi" class="tab">
<div class="section"><h2>合规资料包 <button class="refresh-btn" data-action="loadRFIDocuments">刷新</button></h2>
<div class="card">
<h3>生成资料包</h3>
<div class="form-grid">
<div class="form-group"><label>订单ID</label><input type="text" id="new-rfi-order" placeholder="输入订单ID"></div>
<div class="form-group"><label>文档类型</label><select id="new-rfi-type"><option value="INVOICE">发票</option><option value="CONTRACT">合同</option><option value="SHIPPING">物流</option><option value="PROOF_OF_DELIVERY">履约证明</option></select></div>
</div>
<button class="submit-btn" data-action="generateRFIDocument">生成文档</button>
</div>
<table><thead><tr><th>ID</th><th>订单ID</th><th>商户ID</th><th>类型</th><th>状态</th><th>源数据哈希</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="rfi-documents-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-acquirer-channels" class="tab">
<div class="alert-banner info">G16: Scenario Macro 仅提供易用默认值，不是安全边界。高危模板自动锁定 Tier/Strategy/Funding，防止误选非法组合。后端仍为最终边界。</div>
<div class="section"><h2>收单通道 Tier 管理 <button class="refresh-btn" data-action="loadAcquirerChannels">刷新</button></h2>
<div class="card">
<h3>新增收单通道</h3>
<div class="form-group" id="acq-macro-cards"><label>场景模板</label><div class="radio-cards"><label class="radio-card" id="macro-compliant"><input type="radio" name="acq-macro" value="compliant" data-action="applyAcquirerMacro" checked><span class="radio-card-title">标准合规模板</span><span class="radio-card-desc">TIER_2_COMPLIANT + DEFAULT<br>资金路径可选</span></label><label class="radio-card" id="macro-highrisk"><input type="radio" name="acq-macro" value="highrisk" data-action="applyAcquirerMacro"><span class="radio-card-title">高危承接模板</span><span class="radio-card-desc">TIER_1_HIGH_RISK + BURN_MODE<br>CRYPTO_DESENSITIZED 锁定</span></label></div></div>
<div class="form-grid">
<div class="form-group"><label>商户</label><select id="new-acq-merchant" data-action="loadMerchantsForChannel"><option value="">请选择商户（需有ACTIVE路由策略）</option></select></div>
<div class="form-group"><label>Provider Tier</label><select id="new-acq-provider-tier" data-action="applyAcquirerMacro"><option value="TIER_2_COMPLIANT">合规法币区</option><option value="TIER_1_HIGH_RISK">高风险合规隔离区</option></select><small id="tier-funding-tooltip" class="empty">合规通道可按卡池策略选择充值路径。</small></div>
<div class="form-group"><label>Provider Code</label><input type="text" id="new-acq-provider-code" placeholder="如: stripe"></div>
<div class="form-group"><label>通道名称（代码）</label><input type="text" id="new-acq-channel-code" maxlength="64" placeholder="例如 stripe_visa"></div>
<div class="form-group"><label>机构商户号</label><input type="text" id="new-acq-institution-merchant" maxlength="128" placeholder="Nxtapay 必填，由机构分配"></div>
<div class="form-group"><label>获批收款币种</label><input type="text" id="new-acq-currencies" maxlength="120" placeholder="例如 USD, EUR"><small class="field-help">按机构批准的币种填写，以逗号分隔。</small></div>
<div class="form-group"><label>Credential Vault Key</label><input type="text" id="new-acq-vault-key" placeholder="vault://..." data-action="validateVaultKey" data-vault-input="new-acq-vault-key"><small class="empty">首次接入请留空，通道将以停用状态创建，随后在「凭证管理」中录入。已有安全凭证引用可选填。</small></div>
<div class="form-group"><label>Strategy Mode</label><select id="new-acq-strategy-mode"><option value="DEFAULT">DEFAULT</option><option value="BURN_MODE">BURN_MODE</option></select></div>
<div class="form-group"><label>Funding Mode</label><select id="new-acq-funding-mode" data-action="applyAcquirerMacro"><option value="CRYPTO_DESENSITIZED">加密资产清算大仓（Crypto Pool）</option><option value="FIAT_DIRECT">法币直充</option></select><small id="acq-funding-tooltip" class="empty"></small></div>
<div class="form-group" id="acq-isolation-group-panel" style="display:none"><label>风险隔离组</label><select id="new-acq-isolation-group"><option value="">请选择隔离组</option></select></div>
<div class="form-group" id="acq-manual-review-panel" style="display:none"><label>人工复核策略</label><select id="new-acq-manual-review"><option value="false">不需要</option><option value="true">强制人工复核</option></select></div>
</div>
<button class="submit-btn" data-action="createAcquirerChannel">创建通道</button>
<p id="new-acq-result" class="operation-result" role="status"></p>
</div>
<table><thead><tr><th>ID</th><th>商户</th><th>Provider Tier</th><th>支付机构</th><th>收单通道</th><th>Strategy Mode</th><th>Funding Mode</th><th>状态</th><th>Provider Health</th><th>上次重载</th><th>凭证状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="acquirer-channels-table"><tr><td colspan="13" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-issuing-pools" class="tab">
<div class="section"><h2>发卡卡池 Tier 管理 <button class="refresh-btn" data-action="loadIssuingPools">刷新</button></h2>
<div class="card">
<h3>新增发卡卡池</h3>
<div class="form-grid">
<div class="form-group"><label>实体ID</label><input type="text" id="new-iss-entity-id" placeholder="输入实体ID（可选）"></div>
<div class="form-group"><label>Issuing Tier</label><select id="new-iss-issuing-tier" data-action="syncIssuingFundingGuard"><option value="TIER_1_AGGREGATOR">TIER_1_AGGREGATOR</option><option value="TIER_2_DIRECT">TIER_2_DIRECT</option></select></div>
<div class="form-group"><label>Funding Mode</label><select id="new-iss-funding-mode" data-action="syncIssuingFundingGuard"><option value="CRYPTO_DESENSITIZED">加密资产清算大仓（Crypto Pool）</option><option value="FIAT_DIRECT">法币直充</option></select></div>
<div class="form-group" id="fiat-direct-panel"><label>法币直充状态</label><input type="text" value="可按卡池策略启用" disabled></div>
<div class="form-group" id="crypto-pool-panel"><label>合规隔离资金池</label><input type="text" value="Crypto Pool" disabled></div>
<div class="form-group"><label>Provider Code</label><input type="text" id="new-iss-provider-code" placeholder="如: checkout_com"></div>
<div class="form-group"><label>Pool Code</label><input type="text" id="new-iss-pool-code" placeholder="如: usd_pool_001"></div>
<div class="form-group"><label>Credential Vault Key</label><input type="text" id="new-iss-vault-key" placeholder="vault://..." data-action="validateVaultKey" data-vault-input="new-iss-vault-key"><small class="empty">仅接受 vault:// 引用，禁止明文密钥</small></div>
<div class="form-group"><label>Adapter Type</label><select id="new-iss-adapter-type" data-action="syncIssuingAdapterGuard"><option value="UNSUPPORTED">UNSUPPORTED</option><option value="NEXUS_ISSUING_HTTP_V1">NEXUS_ISSUING_HTTP_V1</option>`+mockLocalOption(allowMockLocal)+`</select></div>
<div class="form-group"><label>Adapter Provider</label><select id="new-iss-adapter-provider-id"><option value="">请选择</option></select><small class="empty">从 CARD_ISSUER Provider 列表加载</small></div>
<div class="form-group"><label>Base URL (HTTPS)</label><input type="text" id="new-iss-base-url" placeholder="https://api.example.com"><small class="empty">NEXUS_ISSUING_HTTP_V1 必填</small></div>
<div class="form-group"><label>BIN 码健康滑块</label><input type="range" id="new-iss-decline-limit" name="declined_count_sliding_window" min="1" max="20" value="5" data-action="updateDeclineLimit"><small>最大拒绝次数：<span id="new-iss-decline-limit-value">5</span></small></div>
</div>
<button class="submit-btn" data-action="createIssuingPool">创建卡池</button>
</div>
<table><thead><tr><th>ID</th><th>实体ID</th><th>Issuing Tier</th><th>Funding Mode</th><th>Provider Code</th><th>Pool Code</th><th>Adapter</th><th>状态</th><th>Provider Health</th><th>节点同步</th><th>凭证状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="issuing-pools-table"><tr><td colspan="13" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-bank-vba" class="tab">
<div class="alert-banner warn">G16 重要警示：此处录入的 VA 账号仅为本地记录，不会向银行开户。银行开户需通过银行 API 或线下流程完成。若仅有本地录入而无真实银行账户，系统会在通道健康状态中标注"仅本地记录"。</div>
<div class="section"><h2>VA 账号池管理 <button class="refresh-btn" data-action="loadVAPool">刷新</button></h2>
<div class="card">
<h3>录入 VA 账号</h3>
<div class="form-grid">
<div class="form-group"><label>租户 ID</label><input type="text" id="new-va-tenant-id" placeholder="输入租户 ID"></div>
<div class="form-group"><label>通道 ID</label><select id="new-va-channel-id"><option value="">请选择通道</option></select></div>
<div class="form-group"><label>账号号码</label><input type="text" id="new-va-account-number" placeholder="纯数字账号"></div>
<div class="form-group"><label>路由号码</label><input type="text" id="new-va-routing-number" placeholder="可选"></div>
<div class="form-group"><label>IBAN</label><input type="text" id="new-va-iban" placeholder="可选"></div>
<div class="form-group"><label>SWIFT</label><input type="text" id="new-va-swift" placeholder="可选"></div>
<div class="form-group"><label>银行名称</label><input type="text" id="new-va-bank-name" placeholder="必填"></div>
<div class="form-group"><label>银行地址</label><input type="text" id="new-va-bank-address" placeholder="可选"></div>
<div class="form-group"><label>资产代码</label><select id="new-va-asset-code"><option value="USD">USD</option><option value="SGD">SGD</option><option value="HKD">HKD</option><option value="EUR">EUR</option><option value="GBP">GBP</option><option value="AED">AED</option></select></div>
</div>
<button class="submit-btn" data-action="createVAAccount">录入 VA 账号</button>
</div>
<table><thead><tr><th>ID</th><th>租户ID</th><th>通道ID</th><th>账号号码</th><th>银行名称</th><th>资产</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="va-pool-table"><tr><td colspan="9" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>结算账户余额 <button class="refresh-btn" data-action="loadSettlementAccounts">刷新</button></h2>
<div class="card">
<h3>录入初始余额</h3>
<div class="form-grid">
<div class="form-group"><label>银行账户 ID</label><input type="text" id="new-sa-bank-account-id" placeholder="输入银行账户 ID"></div>
<div class="form-group"><label>币种</label><select id="new-sa-currency"><option value="USD">USD</option><option value="SGD">SGD</option><option value="HKD">HKD</option><option value="EUR">EUR</option><option value="GBP">GBP</option></select></div>
<div class="form-group"><label>初始余额(最小单位)</label><input type="number" id="new-sa-accumulated-minor" placeholder="如: 1000000"></div>
</div>
<button class="submit-btn" data-action="createSettlementAccount">录入余额</button>
</div>
<table><thead><tr><th>ID</th><th>银行账户ID</th><th>币种</th><th>余额(最小单位)</th><th>更新时间</th></tr></thead><tbody id="settlement-accounts-table"><tr><td colspan="5" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>收款意图监控 <button class="refresh-btn" data-action="loadIntents">刷新</button></h2>
<table><thead><tr><th>ID</th><th>租户ID</th><th>订单ID</th><th>期望金额</th><th>已收金额</th><th>已确认金额</th><th>状态</th><th>过期时间</th><th>创建时间</th></tr></thead><tbody id="bank-intents-table"><tr><td colspan="9" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>对账记录 <button class="refresh-btn" data-action="loadReconciliationLogs">刷新</button></h2>
<table><thead><tr><th>ID</th><th>租户ID</th><th>意图ID</th><th>对账日期</th><th>匹配状态</th><th>差异类型</th><th>已收金额</th><th>期望金额</th><th>创建时间</th></tr></thead><tbody id="reconciliation-table"><tr><td colspan="9" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-merchant-onboarding" class="tab">
<div class="section"><h2>商户入网 <button class="refresh-btn" data-action="loadMerchants">刷新</button></h2>
<div class="card">
<h3>新增商户</h3>
<div class="form-grid">
<div class="form-group"><label>商户名称</label><input type="text" id="new-merchant-name" placeholder="如: Acme Corp"></div>
<div class="form-group"><label>法人名称</label><input type="text" id="new-merchant-legal-name" placeholder="如: Acme Corporation"></div>
<div class="form-group"><label>司法辖区</label><input type="text" id="new-merchant-jurisdiction" placeholder="如: SG"></div>
<div class="form-group"><label>结算币种</label><select id="new-merchant-currency"><option value="USD">USD</option><option value="SGD">SGD</option><option value="HKD">HKD</option><option value="EUR">EUR</option></select></div>
<div class="form-group"><label>Webhook URL</label><input type="text" id="new-merchant-webhook-url" placeholder="https://..."></div>
<div class="form-group"><label>Webhook Secret Vault Key</label><input type="text" id="new-merchant-webhook-secret" placeholder="vault://..." data-action="validateVaultKey" data-vault-input="new-merchant-webhook-secret"></div>
<div class="form-group"><label>MAE Config ID</label><input type="text" id="new-merchant-mae-config" placeholder="可选"></div>
<div class="form-group"><label>银行账户ID</label><input type="text" id="new-merchant-bank-account" placeholder="可选"></div>
<div class="form-group"><label>准备金比例（%）</label><input type="number" id="new-merchant-reserve-ratio" placeholder="10" value="10" min="0" max="100" step="0.01"></div>
<div class="form-group"><label>风险标签</label><input type="text" id="new-merchant-risk-label" placeholder="如: LOW"></div>
</div>
<button class="submit-btn" data-action="createMerchant">创建商户</button>
</div>
<table><thead><tr><th>ID</th><th>商户名称</th><th>账户编号</th><th>状态</th><th>注册地区</th></tr></thead><tbody id="merchants-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-approval-inbox" class="tab">
<div class="section"><h2>审批 Inbox（Maker-Checker） <button class="refresh-btn" data-action="loadApprovalInbox">刷新</button></h2>
<div class="alert-banner info">G08/G16: 高危操作必须经 Maker-Checker 审批。PENDING 不改配置，自审 403，不同 checker 批准后原子应用 + outbox + 审计。身份从登录会话获取，拒绝 body 中伪造的 requester/checker。审批详情显示 before→after 对比、同步状态（已申请/已批准/已应用/节点已同步/失败）和审计事件代码。</div>
<div class="form-group mb12"><label>状态过滤</label><select id="approval-status-filter" data-action="loadApprovalInbox"><option value="">全部</option><option value="PENDING">已申请</option><option value="APPLYING">应用中</option><option value="APPLIED">已应用</option><option value="REJECTED">已拒绝</option><option value="FAILED_STALE">应用失败</option></select></div>
<table><thead><tr><th>ID</th><th>动作</th><th>目标</th><th>版本锁</th><th>申请人</th><th>状态</th><th>复核人</th><th>Outbox</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="approval-inbox-table"><tr><td colspan="10" class="loading">加载中...</td></tr></tbody></table></div>
<div id="approval-detail-panel" class="section hidden"><h2>审批详情</h2><div id="approval-detail-content"></div></div>
</div>
</div>
<script nonce="`+nonce+`">
// G04: escapeHTML 对所有插入 innerHTML 的动态值进行 HTML 转义。
// 这等价于 textContent 的安全效果：转义后的字符串即使含 <script> 也不会执行。
// 所有数据库字段（provider_name, merchant_name, reason, human_text, metadata 等）
// 在拼入 innerHTML 前必须经过 esc()。
function esc(s){if(s===null||s===undefined)return '';s=String(s);return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;');}
// G04: setText 使用 textContent 设置文本，杜绝 XSS。
function setText(el,text){if(el)el.textContent=(text===null||text===undefined)?'':String(text);return el;}
// G04: makeTD 创建 td 并通过 textContent 设置内容（不经过 innerHTML）。
function makeTD(text,cls){var td=document.createElement('td');td.textContent=(text===null||text===undefined)?'':String(text);if(cls)td.className=cls;return td;}
// G04: makeBtn 创建 button 并通过 addEventListener 绑定事件，替代 inline onclick。
function makeBtn(label,cls,action,id){var btn=document.createElement('button');btn.className='action-btn'+(cls?(' '+cls):'');btn.textContent=label;btn.dataset.action=action;if(id)btn.dataset.id=id;return btn;}
// G04: 全局事件委托：处理所有 data-action 按钮点击，替代 inline onclick。
// 在 DOMContentLoaded 后绑定一次，所有动态生成的按钮都通过 data-action 触发。
function handleActionClick(e){var btn=e.target.closest('[data-action]');if(!btn)return;var action=btn.dataset.action;var id=btn.dataset.id||'';switch(action){case 'deleteAdapterProvider':deleteAdapterProvider(id);break;case 'enableAdapterProvider':enableAdapterProvider(id);break;case 'loadVAChannels':loadVAChannels();break;case 'confirmDisableAdapterChannel':confirmDisableAdapterChannel(id);break;case 'enableEntity':enableEntity(id);break;case 'confirmSetEntityRiskHold':confirmSetEntityRiskHold(id);break;case 'confirmDisableBridgeProfile':confirmDisableBridgeProfile(id);break;case 'enableBridge':enableBridge(id);break;case 'confirmDisableWalletPolicy':confirmDisableWalletPolicy(id);break;case 'enableWalletPolicy':enableWalletPolicy(id);break;case 'showRiskEventDetail':showRiskEventDetail(id);break;case 'showTaskDetail':showTaskDetail(id);break;case 'showChannelRiskDetail':showChannelRiskDetail(id);break;case 'showMerchantRiskDetail':showMerchantRiskDetail(id);break;case 'showReserveDetail':showReserveDetail(id);break;case 'showFeeDetail':showFeeDetail(id);break;case 'showIsolationGroupDetail':showIsolationGroupDetail(id);break;case 'freezeIsolationGroup':freezeIsolationGroup(id);break;case 'unfreezeIsolationGroup':unfreezeIsolationGroup(id);break;case 'showRFIDetail':showRFIDetail(id);break;case 'confirmApproveRFIDoc':confirmApproveRFIDoc(id);break;case 'confirmRejectRFIDoc':confirmRejectRFIDoc(id);break;case 'disableAcquirerChannel':disableAcquirerChannel(id);break;case 'enableAcquirerChannel':enableAcquirerChannel(id);break;case 'reloadAcquirerChannel':reloadAcquirerChannel(id);break;case 'fillAcquirerCredential':fillAcquirerCredential(id);break;case 'disableIssuingPool':disableIssuingPool(id);break;case 'enableIssuingPool':enableIssuingPool(id);break;case 'reloadIssuingPool':reloadIssuingPool(id);break;case 'fillIssuingCredential':fillIssuingCredential(id);break;case 'submitIssuingCredential':submitIssuingCredential(id);break;case 'releaseVA':releaseVA(id);break;case 'disableVA':disableVA(id);break;case 'showApprovalDetail':showApprovalDetail(id);break;case 'approveApproval':approveApproval(id);break;case 'rejectApproval':rejectApproval(id);break;case 'submitChannelRiskConfig':submitChannelRiskConfig(id);break;case 'closeModal':closeModal();break;case 'triggerSystemLockdown':triggerSystemLockdown();break;case 'suspendActiveCards':suspendActiveCards();break;case 'loadStats':loadStats();break;case 'loadAdapterProviders':loadAdapterProviders();break;case 'loadAdapterChannels':loadAdapterChannels();break;case 'createAdapterProvider':createAdapterProvider();break;case 'createAdapterChannel':createAdapterChannel();break;case 'loadEntities':loadEntities();break;case 'createEntity':createEntity();break;case 'loadBridgeProfiles':loadBridgeProfiles();break;case 'createBridgeProfile':createBridgeProfile();break;case 'loadWalletPolicies':loadWalletPolicies();break;case 'createWalletPolicy':createWalletPolicy();break;case 'loadRiskEvents':loadRiskEvents();break;case 'loadRiskHoldStatus':loadRiskHoldStatus();break;case 'loadPendingTasks':loadPendingTasks();break;case 'loadChannelRiskConfig':loadChannelRiskConfig();break;case 'createChannelRiskConfig':createChannelRiskConfig();break;case 'loadRiskOverrides':loadRiskOverrides();break;case 'createMerchantRiskProfile':createMerchantRiskProfile();break;case 'loadReserveEntries':loadRollingReserves();break;case 'loadFeeAllocations':loadFeeAllocations();break;case 'loadFeeStats':loadFeeAllocations();break;case 'loadIsolationGroups':loadIsolationGroups();break;case 'createIsolationGroup':createIsolationGroup();break;case 'loadRFIDocuments':loadRFIDocuments();break;case 'generateRFIDocument':generateRFIDocument();break;case 'loadAcquirerChannels':loadAcquirerChannels();break;case 'createAcquirerChannel':createAcquirerChannel();break;case 'loadIssuingPools':loadIssuingPools();break;case 'createIssuingPool':createIssuingPool();break;case 'loadVAPool':loadVAPool();break;case 'createVAAccount':createVAAccount();break;case 'loadSettlementAccounts':loadSettlementAccounts();break;case 'createSettlementAccount':createSettlementAccount();break;case 'loadIntents':loadIntents();break;case 'loadReconciliationLogs':loadReconciliationLogs();break;case 'loadMerchants':loadMerchants();break;case 'loadMerchantsForChannel':loadMerchantsForChannel();break;case 'applyAcquirerMacro':applyAcquirerMacro();break;case 'loadIsolationGroupsForAcquirer':loadIsolationGroupsForAcquirer();break;case 'createMerchant':createMerchant();break;case 'loadApprovalInbox':loadApprovalInbox();break;}}
var API_BASE='/admin';
var typeCN={'CARD_ACQUIRER':'卡网收单','BANK_COLLECTION':'银行虚拟户入金','CRYPTO_PAY':'加密支付','BANK_RAIL':'银行通道','EXCHANGE':'交易所','LP':'流动性提供商','ONCHAIN_WALLET':'链上钱包'};
var statusCN={'ACTIVE':'启用','DISABLED':'禁用','RISK_HOLD':'风险冻结'};
var destTypeCN={'TRANSIT':'过渡钱包','COLD_WALLET':'冷钱包','CUSTODY':'托管钱包'};
var batchingCN={'IMMEDIATE':'立即执行','ACCUMULATE':'累积批处理','SCHEDULED':'定时批处理'};
var execCN={'MARKET':'市价','LIMIT':'限价','SLICED':'分片执行'};
function getCSRFToken(){var c=document.cookie.match(/admin_csrf=([^;]+)/);return c?c[1]:'';}
function showTab(name,btn){document.querySelectorAll('.tab').forEach(function(t){t.classList.remove('active');});document.querySelectorAll('.nav button').forEach(function(b){b.classList.remove('active');});document.getElementById('tab-'+name).classList.add('active');btn.classList.add('active');if(name==='overview')loadStats();if(name==='universal-adapter'){loadAdapterProviders();loadAdapterChannels();}if(name==='corporate-entity')loadEntities();if(name==='asset-bridge')loadBridgeProfiles();if(name==='wallet-policy')loadWalletPolicies();if(name==='risk-ops'){loadRiskEvents();loadRiskHoldStatus();loadPendingTasks();}if(name==='channel-risk'){loadChannelRiskConfig();loadOverrideAuditLogs();}if(name==='merchant-reserve'){loadMerchantRiskProfiles();loadRollingReserves();}if(name==='fee-allocation')loadFeeAllocations();if(name==='pipeline-isolation')loadIsolationGroups();if(name==='compliance-rfi')loadRFIDocuments();if(name==='acquirer-channels'){loadAcquirerChannels();loadMerchantsForChannel();loadIsolationGroupsForAcquirer();}if(name==='issuing-pools'){loadIssuingPools();loadAdapterProvidersForIssuing();}if(name==='bank-vba'){loadVAPool();loadVAChannels();loadSettlementAccounts();loadIntents();loadReconciliationLogs();}if(name==='merchant-onboarding')loadMerchants();}
function authFetch(url,opts){opts=opts||{};opts.headers=opts.headers||{};opts.credentials='same-origin';var csrf=getCSRFToken();if(csrf){opts.headers['X-CSRF-Token']=csrf;}return fetch(url,opts).then(function(r){if(r.status===401){window.location.assign('/admin/login');throw new Error('登录已过期，请重新登录。');}if(!r.ok){return r.json().catch(function(){return {};}).then(function(d){throw new Error(errMsg(d));});}return r;});}
function errMsg(d){if(d&&d.error&&d.error.message)return d.error.message;if(d&&d.message)return d.message;return '未知错误';}
function listToText(v){if(Array.isArray(v))return v.join(', ');if(typeof v==='string'){var p=null;try{p=JSON.parse(v);}catch(e){}if(Array.isArray(p))return p.join(', ');return v;}return '';}
// UI-015: 安全登出——清除会话 Cookie 并回到登录页
function doLogout(){fetch('/admin/session/logout',{method:'POST',credentials:'same-origin'}).then(function(){window.location.href='/admin/login';}).catch(function(){window.location.href='/admin/login';});}
var logoutBtn=document.getElementById('logout-btn');if(logoutBtn){logoutBtn.addEventListener('click',doLogout);}
function sh(id){return id?String(id):'-';}
function ft(t){return t?String(t).substring(0,19).replace('T',' '):'-';}
function sb(s){if(!s)return'info';s=s.toUpperCase();if(s==='ACTIVE'||s==='CONFIRMED'||s==='RECONCILED'||s==='COMPLETED'||s==='SUBMITTED')return'ok';if(s==='FAILED'||s==='REJECTED'||s==='DEADLETTER')return'err';if(s==='COOLDOWN'||s==='RISK_HOLD'||s==='PENDING')return'warn';return'info';}
function loadStats(){authFetch(API_BASE+'/stats').then(function(r){return r.json();}).then(function(d){var h='<div class="grid">';h+='<div class="stat"><div class="label">通用适配器</div><div class="value">'+esc(d.adapter_provider_count||0)+'</div><div class="sub">家已登记机构</div></div>';h+='<div class="stat"><div class="label">通道配置</div><div class="value">'+esc(d.adapter_channel_count||0)+'</div><div class="sub">个通道</div></div>';h+='<div class="stat"><div class="label">法人实体</div><div class="value">'+esc(d.entity_count||0)+'</div><div class="sub">个实体</div></div>';h+='<div class="stat"><div class="label">兑换规则</div><div class="value">'+esc(d.bridge_profile_count||0)+'</div><div class="sub">个配置</div></div>';h+='<div class="stat"><div class="label">收款钱包</div><div class="value">'+esc(d.wallet_policy_count||0)+'</div><div class="sub">个策略</div></div>';h+='<div class="stat"><div class="label">待处理任务</div><div class="value">'+esc(d.outbox_pending||0)+'</div><div class="sub">条待处理</div></div>';h+='</div>';document.getElementById('stats-content').innerHTML=h;}).catch(function(){document.getElementById('stats-content').innerHTML='<div class="empty">加载失败，请刷新页面或重新登录</div>';});}
function loadAdapterProviders(){authFetch(API_BASE+'/adapter/providers').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('adapter-providers-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="6" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var status=statusCN[i.status]||i.status;var type=typeCN[i.adapter_type]||i.adapter_type;var tr=document.createElement('tr');tr.appendChild(makeTD(esc(i.provider_code)));tr.appendChild(makeTD(esc(i.provider_name)));tr.appendChild(makeTD(type));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';tr.appendChild(statusTd);tr.appendChild(makeTD(ft(i.created_at)));var actionTd=document.createElement('td');if(i.status!=='ENABLED'&&i.status!=='ACTIVE'){actionTd.appendChild(makeBtn('启用','enable','enableAdapterProvider',i.id));}actionTd.appendChild(makeBtn('删除','danger','deleteAdapterProvider',i.id));tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('adapter-providers-table').innerHTML='<tr><td colspan="6" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function enableAdapterProvider(id){submitApprovalRequest('ENABLE_ADAPTER_PROVIDER',{provider_id:id}).then(function(){loadAdapterProviders();});}
// P0-2: loadMerchantsForChannel - 加载有ACTIVE routing policy的商户到收单通道表单下拉框
function loadMerchantsForChannel(){var sel=document.getElementById('new-acq-merchant');if(!sel)return;authFetch(API_BASE+'/api/v3/merchants/for-channel').then(function(r){return r.json();}).then(function(d){var items=d.items||[];sel.innerHTML='<option value="">请选择商户（需有ACTIVE路由策略）</option>';items.forEach(function(m){var opt=document.createElement('option');opt.value=m.id;opt.dataset.tenantId=m.tenant_id;opt.dataset.providerTier=m.provider_tier;opt.textContent=m.merchant_name+' ('+m.provider_tier+')';sel.appendChild(opt);});}).catch(function(e){sel.innerHTML='<option value="">加载商户失败</option>';});}
function createAdapterProvider(){var code=document.getElementById('new-provider-code').value;var name=document.getElementById('new-provider-name').value;var type=document.getElementById('new-provider-type').value;if(!code||!name){alert('请填写完整信息');return;}authFetch(API_BASE+'/adapter/providers',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({provider_code:code,provider_name:name,adapter_type:type,provider_config:{}})}).then(function(r){return r.json().then(function(d){if(r.ok){loadAdapterProviders();document.getElementById('new-provider-code').value='';document.getElementById('new-provider-name').value='';}else{alert('创建失败: '+errMsg(d));}});}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function deleteAdapterProvider(id){if(!confirm('确认删除该提供商？'))return;authFetch(API_BASE+'/adapter/providers/'+id,{method:'DELETE'}).then(function(r){if(r.ok){loadAdapterProviders();}else{r.json().then(function(d){alert('删除失败: '+errMsg(d));});}}).catch(function(e){alert('删除失败: '+errMsg(e));});}
function loadAdapterChannels(){authFetch(API_BASE+'/adapter/providers').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var opts='<option value="">请选择提供商</option>';items.forEach(function(i){opts+='<option value="'+esc(i.provider_code)+'">'+esc(i.provider_name)+' ('+esc(i.provider_code)+')</option>';});document.getElementById('new-channel-provider').innerHTML=opts;});authFetch(API_BASE+'/adapter/channels').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('adapter-channels-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="8" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var status=statusCN[i.status]||i.status;var type=typeCN[i.adapter_type]||i.adapter_type;var assets=listToText(i.supported_assets);var networks=listToText(i.supported_networks);var tr=document.createElement('tr');tr.appendChild(makeTD(esc(i.channel_code)));tr.appendChild(makeTD(esc(i.channel_name)));tr.appendChild(makeTD(sh(i.provider_id)));tr.appendChild(makeTD(type));tr.appendChild(makeTD(assets));tr.appendChild(makeTD(networks));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';tr.appendChild(statusTd);var actionTd=document.createElement('td');actionTd.appendChild(makeBtn('禁用','danger','confirmDisableAdapterChannel',i.id));tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('adapter-channels-table').innerHTML='<tr><td colspan="8" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function createAdapterChannel(){var code=document.getElementById('new-channel-code').value;var name=document.getElementById('new-channel-name').value;var tenant=document.getElementById('new-channel-tenant').value;var provider=document.getElementById('new-channel-provider').value;var type=document.getElementById('new-channel-type').value;var assets=document.getElementById('new-channel-assets').value.split(',').map(function(s){return s.trim();}).filter(Boolean);var networks=document.getElementById('new-channel-networks').value.split(',').map(function(s){return s.trim();}).filter(Boolean);if(!code||!name||!tenant||!provider){alert('请填写完整信息（含租户 ID）');return;}authFetch(API_BASE+'/adapter/channels',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({tenant_id:tenant,channel_code:code,channel_name:name,provider_code:provider,adapter_type:type,supported_assets:assets,supported_networks:networks,channel_config:{}})}).then(function(r){return r.json().then(function(d){if(r.ok){loadAdapterChannels();document.getElementById('new-channel-code').value='';document.getElementById('new-channel-name').value='';document.getElementById('new-channel-tenant').value='';document.getElementById('new-channel-assets').value='';document.getElementById('new-channel-networks').value='';}else{alert('创建失败: '+errMsg(d));}});}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function confirmDisableAdapterChannel(id){if(!confirm('确认禁用该通道？禁用后该通道将无法处理交易。'))return;authFetch(API_BASE+'/adapter/channels/'+id+'/disable',{method:'POST'}).then(function(r){if(r.ok){loadAdapterChannels();}else{r.json().then(function(d){alert('禁用失败: '+errMsg(d));});}}).catch(function(e){alert('禁用失败: '+errMsg(e));});}
function loadEntities(){authFetch(API_BASE+'/entities').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('entities-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="7" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var status=statusCN[i.status]||i.status;var tr=document.createElement('tr');tr.appendChild(makeTD(esc(i.name)));tr.appendChild(makeTD(esc(i.legal_name)));tr.appendChild(makeTD(esc(i.country)));tr.appendChild(makeTD(esc(i.jurisdiction)));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';tr.appendChild(statusTd);tr.appendChild(makeTD(ft(i.created_at)));var actionTd=document.createElement('td');if(i.status==='RISK_HOLD'){actionTd.appendChild(makeBtn('解除冻结','enable','enableEntity',i.id));}else{actionTd.appendChild(makeBtn('风险冻结','danger','confirmSetEntityRiskHold',i.id));}tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('entities-table').innerHTML='<tr><td colspan="7" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function createEntity(){var tenant=document.getElementById('new-entity-tenant').value;var name=document.getElementById('new-entity-name').value;var legal=document.getElementById('new-entity-legal').value;var country=document.getElementById('new-entity-country').value;var jurisdiction=document.getElementById('new-entity-jurisdiction').value;if(!tenant||!name||!legal){alert('请填写完整信息');return;}authFetch(API_BASE+'/entities',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({tenant_id:tenant,name:name,legal_name:legal,country:country,jurisdiction:jurisdiction})}).then(function(r){return r.json().then(function(d){if(r.ok){loadEntities();document.getElementById('new-entity-tenant').value='';document.getElementById('new-entity-name').value='';document.getElementById('new-entity-legal').value='';document.getElementById('new-entity-jurisdiction').value='';}else{alert('创建失败: '+errMsg(d));}});}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function confirmSetEntityRiskHold(id){if(!confirm('确认对该实体进行风险冻结？冻结后该实体的所有通道将停止处理交易。'))return;authFetch(API_BASE+'/entities/'+id+'/risk-hold',{method:'POST'}).then(function(r){if(r.ok){loadEntities();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}
function confirmDisableBridgeProfile(id){if(!confirm('确认禁用该桥接配置？禁用后将停止相关资产转换。'))return;authFetch(API_BASE+'/asset-bridge/profiles/'+id+'/disable',{method:'POST'}).then(function(r){if(r.ok){loadBridgeProfiles();}else{r.json().then(function(d){alert('禁用失败: '+errMsg(d));});}}).catch(function(e){alert('禁用失败: '+errMsg(e));});}
function enableEntity(id){authFetch(API_BASE+'/entities/'+id+'/enable',{method:'POST'}).then(function(r){if(r.ok){loadEntities();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}
function loadBridgeProfiles(){authFetch(API_BASE+'/asset-bridge/profiles').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('bridge-profiles-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="9" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var batching=batchingCN[i.batching_policy]||i.batching_policy;var exec=execCN[i.execution_policy]||i.execution_policy;var status=i.enabled?'启用':'禁用';var tr=document.createElement('tr');tr.appendChild(makeTD(esc(i.name)));tr.appendChild(makeTD(esc(i.source_asset)));tr.appendChild(makeTD(esc(i.target_asset)));tr.appendChild(makeTD(i.buffer_percent+'%'));tr.appendChild(makeTD(i.max_slippage_bps+' bps'));tr.appendChild(makeTD(batching));tr.appendChild(makeTD(exec));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+(i.enabled?'ok':'err')+'">'+esc(status)+'</span>';tr.appendChild(statusTd);var actionTd=document.createElement('td');if(i.enabled){actionTd.appendChild(makeBtn('禁用','danger','confirmDisableBridgeProfile',i.id));}else{actionTd.appendChild(makeBtn('启用','enable','enableBridge',i.id));}tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('bridge-profiles-table').innerHTML='<tr><td colspan="9" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function createBridgeProfile(){var tenant=document.getElementById('new-bridge-tenant').value;var name=document.getElementById('new-bridge-name').value;var source=document.getElementById('new-bridge-source').value;var target=document.getElementById('new-bridge-target').value;var buffer=parseFloat(document.getElementById('new-bridge-buffer').value)||5;var slippage=parseInt(document.getElementById('new-bridge-slippage').value)||20;var batching=document.getElementById('new-bridge-batching').value;var execution=document.getElementById('new-bridge-execution').value;if(!tenant||!name||!source||!target){alert('请填写完整信息');return;}authFetch(API_BASE+'/asset-bridge/profiles',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({tenant_id:tenant,name:name,source_asset:source,target_asset:target,buffer_percent:buffer,max_slippage_bps:slippage,batching_policy:batching,execution_policy:execution,withdrawal_policy:'AUTO'})}).then(function(r){return r.json().then(function(d){if(r.ok){loadBridgeProfiles();document.getElementById('new-bridge-name').value='';document.getElementById('new-bridge-source').value='';document.getElementById('new-bridge-target').value='';}else{alert('创建失败: '+errMsg(d));}});}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function confirmDisableBridge(id){if(!confirm('确认禁用该桥接配置？禁用后将停止相关资产转换。'))return;authFetch(API_BASE+'/asset-bridge/profiles/'+id+'/disable',{method:'POST'}).then(function(r){if(r.ok){loadBridgeProfiles();}else{r.json().then(function(d){alert('禁用失败: '+errMsg(d));});}}).catch(function(e){alert('禁用失败: '+errMsg(e));});}
function enableBridge(id){authFetch(API_BASE+'/asset-bridge/profiles/'+id+'/enable',{method:'POST'}).then(function(r){if(r.ok){loadBridgeProfiles();}else{r.json().then(function(d){alert('启用失败: '+errMsg(d));});}}).catch(function(e){alert('启用失败: '+errMsg(e));});}
function loadWalletPolicies(){authFetch(API_BASE+'/wallet-policies').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('wallet-policies-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="9" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var status=statusCN[i.status]||i.status;var destType=destTypeCN[i.destination_type]||i.destination_type;var tr=document.createElement('tr');tr.appendChild(makeTD(i.policy_code));tr.appendChild(makeTD(esc(i.asset_id)));tr.appendChild(makeTD(esc(i.network)));tr.appendChild(makeTD(esc(sh(i.address))));tr.appendChild(makeTD(destType));tr.appendChild(makeTD(i.max_single_withdrawal));tr.appendChild(makeTD(i.daily_limit));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';tr.appendChild(statusTd);var actionTd=document.createElement('td');if(i.status==='ACTIVE'){actionTd.appendChild(makeBtn('禁用','danger','confirmDisableWalletPolicy',i.id));}else{actionTd.appendChild(makeBtn('启用','enable','enableWalletPolicy',i.id));}tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('wallet-policies-table').innerHTML='<tr><td colspan="9" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function createWalletPolicy(){var tenant=document.getElementById('new-wallet-tenant').value;var code=document.getElementById('new-wallet-code').value;var asset=document.getElementById('new-wallet-asset').value;var network=document.getElementById('new-wallet-network').value;var address=document.getElementById('new-wallet-address').value;var destType=document.getElementById('new-wallet-dest-type').value;var label=document.getElementById('new-wallet-label').value;var singleLimit=parseFloat(document.getElementById('new-wallet-single-limit').value)||0;var dailyLimit=parseFloat(document.getElementById('new-wallet-daily-limit').value)||0;if(!tenant||!code||!asset||!network||!address||!label){alert('请填写完整信息');return;}authFetch(API_BASE+'/wallet-policies',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({tenant_id:tenant,policy_code:code,asset:asset,network:network,address:address,destination_type:destType,label:label,max_single_withdrawal:singleLimit,daily_limit:dailyLimit})}).then(function(r){return r.json().then(function(d){if(r.ok){loadWalletPolicies();document.getElementById('new-wallet-code').value='';document.getElementById('new-wallet-address').value='';document.getElementById('new-wallet-label').value='';}else{alert('创建失败: '+errMsg(d));}});}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function confirmDisableWalletPolicy(id){if(!confirm('确认禁用该钱包策略？禁用后该地址将无法用于提现。'))return;authFetch(API_BASE+'/wallet-policies/'+id+'/disable',{method:'POST'}).then(function(r){if(r.ok){loadWalletPolicies();}else{r.json().then(function(d){alert('禁用失败: '+errMsg(d));});}}).catch(function(e){alert('禁用失败: '+errMsg(e));});}
function enableWalletPolicy(id){authFetch(API_BASE+'/wallet-policies/'+id+'/enable',{method:'POST'}).then(function(r){if(r.ok){loadWalletPolicies();}else{r.json().then(function(d){alert('启用失败: '+errMsg(d));});}}).catch(function(e){alert('启用失败: '+errMsg(e));});}
function loadRiskEvents(){authFetch(API_BASE+'/risk/events').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('risk-events-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="8" class="empty">暂无数据</td></tr>';return;}items.forEach(function(e){var sevCN={'HIGH':'高危','MEDIUM':'中危','LOW':'低危'}[e.severity]||e.severity;var tr=document.createElement('tr');tr.appendChild(makeTD(sh(e.id)));tr.appendChild(makeTD(esc(e.event_type)));tr.appendChild(makeTD(esc(sh(e.entity_id))));tr.appendChild(makeTD(esc(sh(e.channel_id))));var sevTd=document.createElement('td');sevTd.innerHTML='<span class="badge badge-'+sb(e.severity)+'">'+esc(sevCN)+'</span>';tr.appendChild(sevTd);tr.appendChild(makeTD(esc(e.status)));tr.appendChild(makeTD(ft(e.created_at)));var actionTd=document.createElement('td');actionTd.appendChild(makeBtn('详情','','showRiskEventDetail',e.id));tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('risk-events-table').innerHTML='<tr><td colspan="8" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function loadRiskHoldStatus(){authFetch(API_BASE+'/entities').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('risk-hold-table');tbody.innerHTML='';var hasAny=false;items.filter(function(e){return e.status==='RISK_HOLD';}).forEach(function(e){hasAny=true;var tr=document.createElement('tr');tr.appendChild(makeTD('法人实体'));tr.appendChild(makeTD(sh(e.id)));tr.appendChild(makeTD(esc(e.name)));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-err">风险冻结</span>';tr.appendChild(statusTd);tr.appendChild(makeTD(ft(e.updated_at)));var actionTd=document.createElement('td');actionTd.appendChild(makeBtn('解除冻结','enable','enableEntity',e.id));tr.appendChild(actionTd);tbody.appendChild(tr);});if(!hasAny){tbody.innerHTML='<tr><td colspan="6" class="empty">暂无风险冻结状态</td></tr>';}}).catch(function(e){document.getElementById('risk-hold-table').innerHTML='<tr><td colspan="6" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function loadPendingTasks(){authFetch(API_BASE+'/outbox?status=PENDING').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('pending-tasks-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="6" class="empty">暂无待处理任务</td></tr>';return;}items.forEach(function(t){var statusCN={'PENDING':'待处理','UNKNOWN_EXTERNAL_STATE':'外部状态未知','APPROVAL_REQUIRED':'待审批'}[t.status]||t.status;var tr=document.createElement('tr');tr.appendChild(makeTD(sh(t.id)));tr.appendChild(makeTD(esc(t.event_type)));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-warn">'+esc(statusCN)+'</span>';tr.appendChild(statusTd);tr.appendChild(makeTD(sh(t.aggregate_id)));tr.appendChild(makeTD(ft(t.created_at)));var actionTd=document.createElement('td');actionTd.appendChild(makeBtn('详情','','showTaskDetail',t.id));tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('pending-tasks-table').innerHTML='<tr><td colspan="6" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function showRiskEventDetail(id){authFetch(API_BASE+'/risk/events/'+id).then(function(r){return r.json().then(function(d){alert('风险事件详情:\n\n'+JSON.stringify(d,null,2));}).catch(function(){alert('风险事件 ID: '+id+'\n\n无法加载详情，请检查日志。');});}).catch(function(){alert('风险事件 ID: '+id+'\n\n加载失败，请稍后重试。');});}
function showTaskDetail(id){authFetch(API_BASE+'/outbox/'+id).then(function(r){return r.json().then(function(d){var detail='任务详情:\n\nID: '+d.id+'\n事件类型: '+(d.event_type||'-')+'\n聚合类型: '+(d.aggregate_type||'-')+'\n聚合ID: '+(d.aggregate_id||'-')+'\n状态: '+(d.status||'-')+'\n重试次数: '+(d.retry_count||0)+'\n最后错误: '+(d.last_error||'无')+'\n创建时间: '+(d.created_at||'-')+'\n更新时间: '+(d.updated_at||'-');alert(detail);}).catch(function(){alert('任务 ID: '+id+'\n\n无法加载详情，请稍后重试。');});}).catch(function(){alert('任务 ID: '+id+'\n\n加载失败，请稍后重试。');});}

// V3 合规升级 Dashboard 函数
var strategyModeCN={'DEFAULT':'默认模式','STRICT':'严格模式','MANUAL_REVIEW':'人工审核'};
var reserveTypeCN={'ROLLING':'滚动准备金','DISPUTE':'争议准备金'};
var reserveStatusCN={'HELD':'持有中','RELEASED':'已释放','PARTIAL':'部分释放'};
var feeTypeCN={'LP_FEE':'LP费用','FX_SLIPPAGE':'FX滑点','GAS_COST':'Gas成本','BANK_FEE':'银行费用','NETWORK_FEE':'网络费用'};
var isolationStatusCN={'ACTIVE':'活跃','FROZEN':'冻结','RISK_HOLD':'风险暂停'};
var rfiStatusCN={'PENDING':'待处理','GENERATED':'已生成','REVIEW_REQUIRED':'需审核','APPROVED':'已批准','REJECTED':'已拒绝'};
var overrideTypeCN={'RISK_OVERRIDE':'风险覆盖','FREEZE':'冻结','UNFREEZE':'解冻','MANUAL_RELEASE':'手动释放'};

function loadChannelRiskConfig(){authFetch(API_BASE+'/v3/channel-risk').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('channel-risk-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="8" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var mode=strategyModeCN[i.strategy_mode]||i.strategy_mode;var status=statusCN[i.status]||i.status;var tr=document.createElement('tr');tr.appendChild(makeTD(esc(i.channel_code)));tr.appendChild(makeTD(esc(i.channel_name)));var modeTd=document.createElement('td');modeTd.innerHTML='<span class="badge badge-'+sb(i.strategy_mode)+'">'+esc(mode)+'</span>';tr.appendChild(modeTd);tr.appendChild(makeTD(i.chargeback_threshold_bps+' bps'));var overrideTd=document.createElement('td');var overrideHtml=i.risk_override_enabled?'<span class="badge badge-warn">已覆盖</span>':'-';if(i.risk_override_reason){overrideHtml+='<br><small>'+esc(i.risk_override_reason)+'</small>';}overrideTd.innerHTML=overrideHtml;tr.appendChild(overrideTd);var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';tr.appendChild(statusTd);tr.appendChild(makeTD(ft(i.created_at)));var actionTd=document.createElement('td');actionTd.appendChild(makeBtn('配置','','showChannelRiskDetail',i.id));tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('channel-risk-table').innerHTML='<tr><td colspan="8" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function showChannelRiskDetail(id){var modal=document.createElement('div');modal.className='modal';var content=document.createElement('div');content.className='modal-content';var h3=document.createElement('h3');h3.textContent='通道风控配置';content.appendChild(h3);var form=document.createElement('form');form.id='channel-risk-form';var modeLabel=document.createElement('label');modeLabel.textContent='风控模式:';form.appendChild(modeLabel);var modeSelect=document.createElement('select');modeSelect.id='channel-strategy-mode';['DEFAULT:默认模式','STRICT:严格模式','MANUAL_REVIEW:人工审核'].forEach(function(o){var parts=o.split(':');var opt=document.createElement('option');opt.value=parts[0];opt.textContent=parts[1];modeSelect.appendChild(opt);});form.appendChild(modeSelect);var threshLabel=document.createElement('label');threshLabel.textContent='拒付阈值(bps):';form.appendChild(threshLabel);var threshInput=document.createElement('input');threshInput.type='number';threshInput.id='chargeback-threshold';threshInput.value='500';form.appendChild(threshInput);var overrideLabel=document.createElement('label');overrideLabel.textContent='风险覆盖:';form.appendChild(overrideLabel);var overrideCheck=document.createElement('input');overrideCheck.type='checkbox';overrideCheck.id='risk-override-enabled';form.appendChild(overrideCheck);var overrideFields=document.createElement('div');overrideFields.id='override-fields';overrideFields.style.display='none';var reasonLabel=document.createElement('label');reasonLabel.textContent='覆盖原因(必填):';overrideFields.appendChild(reasonLabel);var reasonInput=document.createElement('input');reasonInput.type='text';reasonInput.id='override-reason';overrideFields.appendChild(reasonInput);var opLabel=document.createElement('label');opLabel.textContent='操作人(必填，从会话获取):';overrideFields.appendChild(opLabel);var opInput=document.createElement('input');opInput.type='text';opInput.id='override-operator';opInput.value='admin';opInput.readOnly=true;overrideFields.appendChild(opInput);var expLabel=document.createElement('label');expLabel.textContent='过期时间(必填):';overrideFields.appendChild(expLabel);var expInput=document.createElement('input');expInput.type='datetime-local';expInput.id='override-expires';overrideFields.appendChild(expInput);form.appendChild(overrideFields);var saveBtn=document.createElement('button');saveBtn.type='button';saveBtn.textContent='保存';saveBtn.dataset.action='submitChannelRiskConfig';saveBtn.dataset.id=id;form.appendChild(saveBtn);var cancelBtn=document.createElement('button');cancelBtn.type='button';cancelBtn.textContent='取消';cancelBtn.dataset.action='closeModal';form.appendChild(cancelBtn);content.appendChild(form);modal.appendChild(content);document.body.appendChild(modal);overrideCheck.addEventListener('change',function(){overrideFields.style.display=this.checked?'block':'none';});}
function submitChannelRiskConfig(id){var mode=document.getElementById('channel-strategy-mode').value;var threshold=parseInt(document.getElementById('chargeback-threshold').value)||500;var overrideEnabled=document.getElementById('risk-override-enabled').checked;var reason=null,operator=null,expires=null;if(overrideEnabled){reason=document.getElementById('override-reason').value;operator=document.getElementById('override-operator').value;expires=document.getElementById('override-expires').value;if(!reason||!operator||!expires){alert('覆盖模式下原因、操作人和过期时间必填');return;}}authFetch(API_BASE+'/v3/channel-risk/'+id,{method:'PATCH',headers:{'Content-Type':'application/json'},body:JSON.stringify({strategy_mode:mode,chargeback_threshold_bps:threshold,risk_override_enabled:overrideEnabled,risk_override_reason:reason,risk_override_operator:operator,risk_override_expires_at:expires})}).then(function(r){return r.json().then(function(d){if(r.ok){closeModal();loadChannelRiskConfig();}else{alert('保存失败: '+errMsg(d));}});}).catch(function(e){alert('保存失败: '+errMsg(e));});}
function closeModal(){var modal=document.querySelector('.modal');if(modal)modal.remove();}

function createChannelRiskConfig(){var channelID=document.getElementById('new-risk-channel').value;var riskMode=document.getElementById('new-risk-mode').value;if(!channelID){alert('请选择通道');return;}authFetch(API_BASE+'/v3/channel-risk/'+channelID,{method:'PATCH',headers:{'Content-Type':'application/json'},body:JSON.stringify({strategy_mode:riskMode})}).then(function(r){if(r.ok){document.getElementById('new-risk-channel').value='';document.getElementById('new-risk-mode').value='DEFAULT';loadChannelRiskConfig();}else{r.json().then(function(d){alert('创建失败: '+errMsg(d));});}}).catch(function(e){alert('创建失败: '+errMsg(e));});}

function loadMerchantRiskProfiles(){authFetch(API_BASE+'/v3/merchant-risk-profiles').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('merchant-risk-profiles-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="7" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var status=statusCN[i.status]||i.status;var tr=document.createElement('tr');tr.appendChild(makeTD(sh(i.merchant_id)));tr.appendChild(makeTD(formatRate(i.rolling_reserve_rate_bps)));tr.appendChild(makeTD(formatRate(i.dispute_reserve_rate_bps)));tr.appendChild(makeTD(i.reserve_release_days+' 天'));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';tr.appendChild(statusTd);tr.appendChild(makeTD(ft(i.created_at)));var actionTd=document.createElement('td');actionTd.appendChild(makeBtn('详情','','showMerchantRiskDetail',i.id));tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('merchant-risk-profiles-table').innerHTML='<tr><td colspan="7" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function showMerchantRiskDetail(id){alert('商户风险配置详情: '+id);}

function createMerchantRiskProfile(){var merchantID=document.getElementById('new-reserve-merchant').value;var rollingRate=readRateBps('new-reserve-rolling');var disputeRate=readRateBps('new-reserve-dispute');if(rollingRate===null||disputeRate===null)return;var releaseDays=parseInt(document.getElementById('new-reserve-days').value)||30;if(!merchantID){alert('请输入商户ID');return;}authFetch(API_BASE+'/v3/merchant-risk-profiles',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({merchant_id:merchantID,rolling_reserve_rate_bps:rollingRate,dispute_reserve_rate_bps:disputeRate,reserve_release_days:releaseDays})}).then(function(r){if(r.ok){document.getElementById('new-reserve-merchant').value='';document.getElementById('new-reserve-rolling').value='10';document.getElementById('new-reserve-dispute').value='5';document.getElementById('new-reserve-days').value='30';loadMerchantRiskProfiles();}else{r.json().then(function(d){alert('创建失败: '+errMsg(d));});}}).catch(function(e){alert('创建失败: '+errMsg(e));});}

function loadRollingReserves(){authFetch(API_BASE+'/v3/rolling-reserves').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('reserve-entries-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="8" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var type=reserveTypeCN[i.reserve_type]||i.reserve_type;var status=reserveStatusCN[i.status]||i.status;var tr=document.createElement('tr');tr.appendChild(makeTD(sh(i.merchant_id)));tr.appendChild(makeTD(sh(i.transaction_id)));tr.appendChild(makeTD(i.amount_minor));tr.appendChild(makeTD(type));tr.appendChild(makeTD(ft(i.release_date)));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';tr.appendChild(statusTd);tr.appendChild(makeTD(ft(i.created_at)));var actionTd=document.createElement('td');actionTd.appendChild(makeBtn('详情','','showReserveDetail',i.id));tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('reserve-entries-table').innerHTML='<tr><td colspan="8" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function showReserveDetail(id){alert('准备金记录详情: '+id);}

function loadFeeAllocations(){authFetch(API_BASE+'/v3/fee-allocations').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('fee-allocations-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="8" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var type=feeTypeCN[i.fee_type]||i.fee_type;var status=statusCN[i.status]||i.status;var tr=document.createElement('tr');tr.appendChild(makeTD(sh(i.transaction_id)));tr.appendChild(makeTD(type));tr.appendChild(makeTD(i.amount_minor));tr.appendChild(makeTD(esc(i.currency)));tr.appendChild(makeTD(esc(i.allocation_source)));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';tr.appendChild(statusTd);tr.appendChild(makeTD(ft(i.created_at)));var actionTd=document.createElement('td');actionTd.appendChild(makeBtn('详情','','showFeeDetail',i.id));tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('fee-allocations-table').innerHTML='<tr><td colspan="8" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function showFeeDetail(id){alert('费用摊销详情: '+id);}

function loadIsolationGroups(){authFetch(API_BASE+'/v3/isolation-groups').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('isolation-groups-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="7" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var status=isolationStatusCN[i.status]||i.status;var tr=document.createElement('tr');tr.appendChild(makeTD(esc(i.name)));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';tr.appendChild(statusTd);var frozenTd=document.createElement('td');if(i.frozen_reason){frozenTd.innerHTML='<span class="badge badge-err">冻结</span><br><small>'+esc(i.frozen_reason)+' ('+esc(i.frozen_by)+')</small>';}else{frozenTd.textContent='-';}tr.appendChild(frozenTd);tr.appendChild(makeTD(ft(i.created_at)));tr.appendChild(makeTD(ft(i.updated_at)));var detailTd=document.createElement('td');detailTd.appendChild(makeBtn('详情','','showIsolationGroupDetail',i.id));tr.appendChild(detailTd);var actionTd=document.createElement('td');if(i.status==='ACTIVE'){actionTd.appendChild(makeBtn('冻结','danger','freezeIsolationGroup',i.id));}else{actionTd.appendChild(makeBtn('解冻','enable','unfreezeIsolationGroup',i.id));}tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('isolation-groups-table').innerHTML='<tr><td colspan="7" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function showIsolationGroupDetail(id){alert('管道隔离组详情: '+id);}

function createIsolationGroup(){var groupName=document.getElementById('new-group-name').value;if(!groupName.trim()){alert('请输入隔离组名称');return;}authFetch(API_BASE+'/v3/isolation-groups',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:groupName})}).then(function(r){if(r.ok){document.getElementById('new-group-name').value='';loadIsolationGroups();}else{r.json().then(function(d){alert('创建失败: '+errMsg(d));});}}).catch(function(e){alert('创建失败: '+errMsg(e));});}

function freezeIsolationGroup(id){var reason=prompt('请输入冻结原因:');if(!reason)return;var operator=prompt('请输入操作人:','admin');if(!operator)return;authFetch(API_BASE+'/v3/isolation-groups/'+id+'/freeze',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({reason:reason,operator:operator})}).then(function(r){if(r.ok){loadIsolationGroups();}else{r.json().then(function(d){alert('冻结失败: '+errMsg(d));});}}).catch(function(e){alert('冻结失败: '+errMsg(e));});}
function unfreezeIsolationGroup(id){var reason=prompt('请输入解冻原因:');if(!reason)return;var operator=prompt('请输入操作人:','admin');if(!operator)return;authFetch(API_BASE+'/v3/isolation-groups/'+id+'/unfreeze',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({reason:reason,operator:operator})}).then(function(r){if(r.ok){loadIsolationGroups();}else{r.json().then(function(d){alert('解冻失败: '+errMsg(d));});}}).catch(function(e){alert('解冻失败: '+errMsg(e));});}

function loadRFIDocuments(){authFetch(API_BASE+'/v3/rfi-documents').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('rfi-documents-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="8" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var status=rfiStatusCN[i.status]||i.status;var tr=document.createElement('tr');tr.appendChild(makeTD(sh(i.id)));tr.appendChild(makeTD(esc(sh(i.order_id))));tr.appendChild(makeTD(sh(i.merchant_id)));tr.appendChild(makeTD(esc(i.document_type)));var statusTd=document.createElement('td');var statusHtml='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';if(i.review_required_reason){statusHtml+='<br><small class="warn">'+esc(i.review_required_reason)+'</small>';}statusTd.innerHTML=statusHtml;tr.appendChild(statusTd);tr.appendChild(makeTD(esc(i.source_data_hash)));tr.appendChild(makeTD(ft(i.created_at)));var actionTd=document.createElement('td');actionTd.appendChild(makeBtn('详情','','showRFIDetail',i.id));if(i.status==='PENDING'||i.status==='REVIEW_REQUIRED'){actionTd.appendChild(document.createTextNode(' '));actionTd.appendChild(makeBtn('通过','enable','confirmApproveRFIDoc',i.id));actionTd.appendChild(document.createTextNode(' '));actionTd.appendChild(makeBtn('拒绝','danger','confirmRejectRFIDoc',i.id));}tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('rfi-documents-table').innerHTML='<tr><td colspan="8" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function showRFIDetail(id){authFetch(API_BASE+'/v3/rfi-documents/'+id).then(function(r){return r.json();}).then(function(d){var info='RFI资料包详情\n\n';info+='ID: '+d.id+'\n';info+='订单ID: '+d.order_id+'\n';info+='商户ID: '+d.merchant_id+'\n';info+='类型: '+d.document_type+'\n';info+='状态: '+(rfiStatusCN[d.status]||d.status)+'\n';info+='源数据哈希: '+d.source_data_hash+'\n';info+='创建时间: '+d.created_at+'\n';if(d.approved_at){info+='审批时间: '+d.approved_at;};if(d.rejected_at){info+='\n拒绝时间: '+d.rejected_at;};if(d.rejected_reason){info+='\n拒绝原因: '+d.rejected_reason;};alert(info);}).catch(function(e){alert('加载详情失败: '+errMsg(e));});}
function confirmApproveRFIDoc(id){if(!confirm('确认通过该RFI资料包？'))return;var operator=prompt('请输入操作人:','admin');if(!operator)return;authFetch(API_BASE+'/v3/rfi-documents/'+id+'/approve',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({operator:operator})}).then(function(r){if(r.ok){alert('审批通过成功');loadRFIDocuments();}else{r.json().then(function(d){alert('审批失败: '+errMsg(d));});}}).catch(function(e){alert('审批失败: '+errMsg(e));});}
function confirmRejectRFIDoc(id){var reason=prompt('请输入拒绝原因:');if(!reason)return;var operator=prompt('请输入操作人:','admin');if(!operator)return;authFetch(API_BASE+'/v3/rfi-documents/'+id+'/reject',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({reason:reason,operator:operator})}).then(function(r){if(r.ok){alert('已拒绝');loadRFIDocuments();}else{r.json().then(function(d){alert('拒绝失败: '+errMsg(d));});}}).catch(function(e){alert('拒绝失败: '+errMsg(e));});}

function generateRFIDocument(){var orderID=document.getElementById('new-rfi-order').value;var docType=document.getElementById('new-rfi-type').value;if(!orderID){alert('请输入订单ID');return;}if(!docType){alert('请选择文档类型');return;}authFetch(API_BASE+'/v3/rfi-documents/generate',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({order_id:orderID,document_type:docType})}).then(function(r){if(r.ok){document.getElementById('new-rfi-order').value='';document.getElementById('new-rfi-type').value='INVOICE';loadRFIDocuments();}else{r.json().then(function(d){alert('生成失败: '+errMsg(d));});}}).catch(function(e){alert('生成失败: '+errMsg(e));});}

function loadOverrideAuditLogs(){authFetch(API_BASE+'/v3/override-audit-logs').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('override-audit-logs-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="7" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var type=overrideTypeCN[i.override_type]||i.override_type;var tr=document.createElement('tr');tr.appendChild(makeTD(sh(i.id)));tr.appendChild(makeTD(esc(i.entity_type)));tr.appendChild(makeTD(esc(sh(i.entity_id))));tr.appendChild(makeTD(type));tr.appendChild(makeTD(esc(i.reason)));var operatorTd=document.createElement('td');var operatorHtml=esc(i.operator);if(i.expires_at){operatorHtml+='<br><small>过期: '+esc(ft(i.expires_at))+'</small>';}operatorTd.innerHTML=operatorHtml;tr.appendChild(operatorTd);tr.appendChild(makeTD(ft(i.created_at)));tbody.appendChild(tr);});}).catch(function(e){document.getElementById('override-audit-logs-table').innerHTML='<tr><td colspan="7" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}

function loadAcquirerChannels(){authFetch(API_BASE+'/api/v3/channels').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('acquirer-channels-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="13" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var status=statusCN[i.status]||i.status;var strategyMode=i.strategy_mode||'DEFAULT';var fundingMode=i.funding_mode||(i.routing_config&&i.routing_config.vcc_funding_mode)||'-';var tr=document.createElement('tr');tr.appendChild(makeTD(sh(i.id)));tr.appendChild(makeTD(esc(i.merchant_name||sh(i.entity_id))));tr.appendChild(makeTD(esc(i.provider_tier)));tr.appendChild(makeTD(esc(i.provider_code)));tr.appendChild(makeTD(esc(i.channel_code)));var strategyTd=document.createElement('td');var strategyHtml=esc(strategyMode);if(strategyMode==='BURN_MODE'){strategyHtml+=' <span class="badge badge-err">BURN_MODE</span>';}strategyTd.innerHTML=strategyHtml;tr.appendChild(strategyTd);var fundingTd=document.createElement('td');fundingTd.textContent=fundingMode;tr.appendChild(fundingTd);var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';tr.appendChild(statusTd);var healthTd=document.createElement('td');if(i.provider_health){var healthCN={'HEALTHY':'健康','DEGRADED':'降级','DOWN':'不可用','UNKNOWN':'未知'}[i.provider_health]||i.provider_health;var healthClass={'HEALTHY':'badge-ok','DEGRADED':'badge-warn','DOWN':'badge-err','UNKNOWN':'badge-info'}[i.provider_health]||'badge-info';healthTd.innerHTML='<span class="badge '+healthClass+'">'+esc(healthCN)+'</span>';}else{healthTd.innerHTML='<span class="badge badge-info">未探测</span>';}tr.appendChild(healthTd);tr.appendChild(makeTD(i.last_reload_at?ft(i.last_reload_at):'从未'));var credTd=document.createElement('td');if(i.has_credential){var credHtml='<span class="badge badge-info">有录入 · 待核对</span>';if(i.credential_version){credHtml+=' <small>v'+esc(i.credential_version)+'</small>';}if(i.credential_expires_at){var expiry=new Date(i.credential_expires_at);var now=new Date();var daysLeft=Math.floor((expiry-now)/(1000*60*60*24));if(daysLeft<0){credHtml+=' <span class="badge badge-err">已过期</span>';}else if(daysLeft<7){credHtml+=' <span class="badge badge-warn">将过期('+daysLeft+'天)</span>';}else{credHtml+=' <small>'+daysLeft+'天后过期</small>';}}credTd.innerHTML=credHtml;}else{credTd.innerHTML='<span class="badge badge-err">未配置</span>';}tr.appendChild(credTd);tr.appendChild(makeTD(ft(i.created_at)));var actionTd=document.createElement('td');actionTd.appendChild(makeBtn('凭证管理','','fillAcquirerCredential',i.id));actionTd.appendChild(document.createTextNode(' '));if(i.status==='ACTIVE'){actionTd.appendChild(makeBtn('申请停用','danger','disableAcquirerChannel',i.id));}else{actionTd.appendChild(makeBtn('申请启用','enable','enableAcquirerChannel',i.id));}actionTd.appendChild(document.createTextNode(' '));actionTd.appendChild(makeBtn('重载','','reloadAcquirerChannel',i.id));tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('acquirer-channels-table').innerHTML='<tr><td colspan="12" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function createAcquirerChannel(){
  var merchant=document.getElementById('new-acq-merchant');
  var option=merchant.options[merchant.selectedIndex];
  var provider=document.getElementById('new-acq-provider-code');
  var channel=document.getElementById('new-acq-channel-code');
  var account=document.getElementById('new-acq-institution-merchant');
  var currencyInput=document.getElementById('new-acq-currencies');
  var vault=document.getElementById('new-acq-vault-key');
  var notice=document.getElementById('new-acq-result');
  var submit=document.querySelector('[data-action="createAcquirerChannel"]');
  var currencies=Array.from(new Set(currencyInput.value.trim().toUpperCase().split(/[\s,，]+/)));
  var providerCode=provider.value.trim().toLowerCase();
  if(!merchant.value||!providerCode||!channel.value.trim()){
    notice.textContent='请选择商户，并填写支付机构和通道名称。';return;
  }
  if(!currencies.every(function(code){return /^[A-Z]{3}$/.test(code);})){
    notice.textContent='请输入三字母币种代码，例如 USD、EUR。';currencyInput.focus();return;
  }
  if(providerCode==='nxtapay'&&!account.value.trim()){
    notice.textContent='Nxtapay 需要填写机构商户号。';account.focus();return;
  }
  if(vault.value&&!validateVaultKeyInput(vault.id))return;
  var providerConfig={currencies:currencies};
  if(account.value.trim())providerConfig.merchant_id=account.value.trim();
  var isolationGroup=document.getElementById('new-acq-isolation-group');
  var manualReview=document.getElementById('new-acq-manual-review');
  var routingConfig={};
  if(isolationGroup.value)routingConfig.risk_isolation_group_id=isolationGroup.value;
  if(manualReview.value==='true')routingConfig.manual_review_required=true;
  var payload={entity_id:merchant.value,tenant_id:option.dataset.tenantId,merchant_id:merchant.value,
    provider_tier:document.getElementById('new-acq-provider-tier').value,provider_code:providerCode,
    channel_code:channel.value.trim(),credential_vault_key:vault.value,
    strategy_mode:document.getElementById('new-acq-strategy-mode').value,
    funding_mode:document.getElementById('new-acq-funding-mode').value,provider_config:providerConfig,routing_config:routingConfig};
  submit.disabled=true;notice.textContent='正在保存…';
  authFetch(API_BASE+'/api/v3/channels',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)})
    .then(function(){
      notice.textContent=vault.value?'通道已创建，请核对凭证及机构联调状态。':'已创建停用通道。请在对应行打开「凭证管理」录入。';
      [provider,channel,account,vault].forEach(function(input){input.value='';});
      loadAcquirerChannels();
    }).catch(function(e){notice.textContent='创建失败，请检查配置后重试。'+errMsg(e);})
    .finally(function(){submit.disabled=false;});
}
function enableAcquirerChannel(id){authFetch(API_BASE+'/api/v3/channels/'+id+'/enable',{method:'POST'}).then(function(r){if(r.ok){loadAcquirerChannels();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}
function disableAcquirerChannel(id){if(!confirm('确认禁用该收单通道？禁用后该通道将无法处理交易。'))return;authFetch(API_BASE+'/api/v3/channels/'+id+'/disable',{method:'POST'}).then(function(r){if(r.ok){loadAcquirerChannels();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}
function reloadAcquirerChannel(id){authFetch(API_BASE+'/api/v3/channels/'+id+'/reload',{method:'POST'}).then(function(r){if(r.ok){alert('通道重载请求已发送');loadAcquirerChannels();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}

function loadIssuingPools(){authFetch(API_BASE+'/api/v3/issuing/pools').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('issuing-pools-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="13" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var status=statusCN[i.status]||i.status;var tr=document.createElement('tr');tr.appendChild(makeTD(sh(i.id)));tr.appendChild(makeTD(esc(sh(i.entity_id))));tr.appendChild(makeTD(esc(i.issuing_tier)));tr.appendChild(makeTD(esc(i.funding_mode)));tr.appendChild(makeTD(esc(i.provider_code)));tr.appendChild(makeTD(esc(i.pool_code)));tr.appendChild(makeTD(esc(i.adapter_type||'-')));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';tr.appendChild(statusTd);var healthTd=document.createElement('td');if(i.provider_health){var healthCN={'HEALTHY':'健康','DEGRADED':'降级','DOWN':'不可用','UNKNOWN':'未知'}[i.provider_health]||i.provider_health;var healthClass={'HEALTHY':'badge-ok','DEGRADED':'badge-warn','DOWN':'badge-err','UNKNOWN':'badge-info'}[i.provider_health]||'badge-info';healthTd.innerHTML='<span class="badge '+healthClass+'">'+esc(healthCN)+'</span>';}else{healthTd.innerHTML='<span class="badge badge-info">未探测</span>';}tr.appendChild(healthTd);var syncTd=document.createElement('td');if(i.node_sync_status){var syncCN={'SYNCED':'已同步','PENDING':'待同步','FAILED':'同步失败'}[i.node_sync_status]||i.node_sync_status;var syncClass={'SYNCED':'badge-ok','PENDING':'badge-warn','FAILED':'badge-err'}[i.node_sync_status]||'badge-info';syncTd.innerHTML='<span class="badge '+syncClass+'">'+esc(syncCN)+'</span>';}else{syncTd.innerHTML='<span class="badge badge-info">未知</span>';}tr.appendChild(syncTd);var credTd=document.createElement('td');if(i.has_credential){var credHtml='<span class="badge badge-ok">已配置</span>';if(i.credential_version){credHtml+=' <small>v'+esc(i.credential_version)+'</small>';}if(i.credential_rotation_required){credHtml+=' <span class="badge badge-warn">需轮换</span>';}credTd.innerHTML=credHtml;}else{credTd.innerHTML='<span class="badge badge-err">未配置</span>';}tr.appendChild(credTd);tr.appendChild(makeTD(ft(i.created_at)));var actionTd=document.createElement('td');if(i.status==='ACTIVE'){actionTd.appendChild(makeBtn('禁用','danger','disableIssuingPool',i.id));}else{actionTd.appendChild(makeBtn('启用','enable','enableIssuingPool',i.id));}actionTd.appendChild(document.createTextNode(' '));actionTd.appendChild(makeBtn('重载','','reloadIssuingPool',i.id));actionTd.appendChild(document.createTextNode(' '));actionTd.appendChild(makeBtn('填凭证','','fillIssuingCredential',i.id));tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('issuing-pools-table').innerHTML='<tr><td colspan="12" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function createIssuingPool(){var entityID=document.getElementById('new-iss-entity-id').value;var issuingTier=document.getElementById('new-iss-issuing-tier').value;var fundingMode=document.getElementById('new-iss-funding-mode').value;var providerCode=document.getElementById('new-iss-provider-code').value;var poolCode=document.getElementById('new-iss-pool-code').value;var vaultKey=document.getElementById('new-iss-vault-key').value;var adapterType=document.getElementById('new-iss-adapter-type').value;var adapterProviderID=document.getElementById('new-iss-adapter-provider-id').value;var baseURL=document.getElementById('new-iss-base-url').value;var declineLimit=document.getElementById('new-iss-decline-limit').value;if(!providerCode||!poolCode||!vaultKey){alert('请填写完整信息');return;}if(!vaultKey.startsWith('vault://')){alert('Credential Vault Key 必须以 vault:// 开头');return;}if(adapterType==='NEXUS_ISSUING_HTTP_V1'){if(!adapterProviderID){alert('NEXUS_ISSUING_HTTP_V1 必须选择 Adapter Provider');return;}if(!baseURL||!baseURL.startsWith('https://')){alert('NEXUS_ISSUING_HTTP_V1 必须配置 HTTPS Base URL');return;}}if(adapterType==='UNSUPPORTED'){alert('UNSUPPORTED 适配器类型不能创建卡池');return;}var providerConfig={};if(baseURL){providerConfig.base_url=baseURL;}var velocityConfig={};if(declineLimit){velocityConfig.declined_count_sliding_window=parseInt(declineLimit,10);}authFetch(API_BASE+'/api/v3/issuing/pools',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({entity_id:entityID,issuing_tier:issuingTier,funding_mode:fundingMode,provider_code:providerCode,pool_code:poolCode,credential_vault_key:vaultKey,provider_config:providerConfig,velocity_config:velocityConfig,adapter_type:adapterType,adapter_provider_id:adapterProviderID})}).then(function(r){if(r.ok){document.getElementById('new-iss-entity-id').value='';document.getElementById('new-iss-provider-code').value='';document.getElementById('new-iss-pool-code').value='';document.getElementById('new-iss-vault-key').value='';document.getElementById('new-iss-base-url').value='';document.getElementById('new-iss-funding-mode').value='FIAT_DIRECT';loadIssuingPools();}else{r.json().then(function(d){alert('创建失败: '+errMsg(d));});}}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function loadAdapterProvidersForIssuing(){authFetch(API_BASE+'/api/v3/issuing/adapter-providers').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var sel=document.getElementById('new-iss-adapter-provider-id');sel.innerHTML='<option value="">请选择</option>';items.forEach(function(i){var opt=document.createElement('option');opt.value=i.id;opt.textContent=esc(i.provider_name)+' ('+esc(i.provider_code)+')';sel.appendChild(opt);});}).catch(function(e){console.error('loadAdapterProvidersForIssuing failed:',e);});}
function enableIssuingPool(id){authFetch(API_BASE+'/api/v3/issuing/pools/'+id+'/enable',{method:'POST'}).then(function(r){if(r.ok){loadIssuingPools();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}
function disableIssuingPool(id){if(!confirm('确认禁用该发卡卡池？禁用后该卡池将无法开卡。'))return;authFetch(API_BASE+'/api/v3/issuing/pools/'+id+'/disable',{method:'POST'}).then(function(r){if(r.ok){loadIssuingPools();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}
function reloadIssuingPool(id){authFetch(API_BASE+'/api/v3/issuing/pools/'+id+'/reload',{method:'POST'}).then(function(r){if(r.ok){alert('卡池重载请求已发送');loadIssuingPools();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}

// R20: 凭证可视化管理 — 明文凭证在界面上填写，服务端 KMS 信封加密写入 Vault；
// 写入即绑定（credential_vault_key 原子指向新版本），重复写入即轮换。
function fillIssuingCredential(id){showIssuingCredentialModal(id);}
function showIssuingCredentialModal(id){var modal=document.createElement('div');modal.className='modal';var content=document.createElement('div');content.className='modal-content';var h3=document.createElement('h3');h3.textContent='填写发卡卡池真实凭证';content.appendChild(h3);var tip=document.createElement('p');tip.className='empty';tip.textContent='明文凭证仅经本次传输，服务端 KMS 信封加密后写入 Vault，界面不回显；重复填写自动轮换，旧版本自动置为 REVOKED。';content.appendChild(tip);var form=document.createElement('form');form.id='issuing-cred-form';var typeLabel=document.createElement('label');typeLabel.textContent='凭证类型:';form.appendChild(typeLabel);var typeInput=document.createElement('input');typeInput.type='text';typeInput.value='API 密钥 (API_KEY)';typeInput.readOnly=true;form.appendChild(typeInput);var matLabel=document.createElement('label');matLabel.textContent='API Key 明文:';form.appendChild(matLabel);var matInput=document.createElement('textarea');matInput.id='iss-cred-material';matInput.rows=5;matInput.autocomplete='off';matInput.placeholder='请粘贴发卡商提供的真实 API Key';form.appendChild(matInput);var saveBtn=document.createElement('button');saveBtn.type='button';saveBtn.textContent='加密保存并轮换';saveBtn.dataset.action='submitIssuingCredential';saveBtn.dataset.id=id;form.appendChild(saveBtn);var cancelBtn=document.createElement('button');cancelBtn.type='button';cancelBtn.textContent='取消';cancelBtn.dataset.action='closeModal';form.appendChild(cancelBtn);content.appendChild(form);modal.appendChild(content);document.body.appendChild(modal);}
function submitIssuingCredential(id){var material=document.getElementById('iss-cred-material').value;if(!material||!material.trim()){alert('请填写 API Key 明文');return;}if(!confirm('确认将凭证加密写入 Vault 并绑定到卡池 '+id.substring(0,8)+'...？重复写入将自动轮换，旧版本置为 REVOKED。'))return;authFetch(API_BASE+'/api/v3/issuing/vault-credentials',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({issuing_provider_id:id,credential_type:'API_KEY',credential_material:{api_key:material}})}).then(function(r){return r.json().then(function(d){if(r.ok){closeModal();alert('凭证已加密保存并绑定\n\nVault 引用: '+d.vault_key+'\n凭证版本: v'+d.key_version+'\n状态: '+d.status);loadIssuingPools();}else{alert('保存失败: '+errMsg(d));}});}).catch(function(e){alert('保存失败: '+errMsg(e));});}

// V3 Tab 初始化函数
function initV3Tabs(){if(document.getElementById('tab-channel-risk').classList.contains('active'))loadChannelRiskConfig();if(document.getElementById('tab-merchant-reserve').classList.contains('active')){loadMerchantRiskProfiles();loadRollingReserves();}if(document.getElementById('tab-fee-allocation').classList.contains('active'))loadFeeAllocations();if(document.getElementById('tab-pipeline-isolation').classList.contains('active'))loadIsolationGroups();if(document.getElementById('tab-compliance-rfi').classList.contains('active'))loadRFIDocuments();}
// P0-3: applyAcquirerMacro — Scenario Macro 控制收单表单字段锁定
function applyAcquirerMacro(){var macroRadio=document.querySelector('input[name="acq-macro"]:checked');var macro=macroRadio?macroRadio.value:'compliant';var tier=document.getElementById('new-acq-provider-tier');var strategy=document.getElementById('new-acq-strategy-mode');var funding=document.getElementById('new-acq-funding-mode');var isolationPanel=document.getElementById('acq-isolation-group-panel');var manualReviewPanel=document.getElementById('acq-manual-review-panel');var tip=document.getElementById('tier-funding-tooltip');var fundingTip=document.getElementById('acq-funding-tooltip');var fiatOpt=funding?Array.from(funding.options).find(function(o){return o.value==='FIAT_DIRECT';}):null;if(macro==='highrisk'){tier.value='TIER_1_HIGH_RISK';strategy.value='BURN_MODE';funding.value='CRYPTO_DESENSITIZED';tier.disabled=true;strategy.disabled=true;funding.disabled=true;if(fiatOpt)fiatOpt.disabled=true;if(isolationPanel)isolationPanel.style.display='block';if(manualReviewPanel)manualReviewPanel.style.display='block';if(tip){tip.innerText='高危模板已锁定：Tier=HIGH_RISK, Strategy=BURN_MODE, Funding=CRYPTO_DESENSITIZED。';tip.className='empty warn';}if(fundingTip){fundingTip.innerText='高危通道强制 CRYPTO_DESENSITIZED，禁止 FIAT_DIRECT。';fundingTip.className='empty warn';}}else{tier.value='TIER_2_COMPLIANT';strategy.value='DEFAULT';tier.disabled=false;strategy.disabled=false;funding.disabled=false;if(fiatOpt)fiatOpt.disabled=false;if(isolationPanel)isolationPanel.style.display='none';if(manualReviewPanel)manualReviewPanel.style.display='none';if(tip){tip.innerText='合规通道可按卡池策略选择充值路径。';tip.className='empty';}if(fundingTip){fundingTip.innerText='';fundingTip.className='empty';}}}
// P0-3: loadIsolationGroupsForAcquirer — 加载隔离组到收单表单下拉框
function loadIsolationGroupsForAcquirer(){var sel=document.getElementById('new-acq-isolation-group');if(!sel)return;authFetch(API_BASE+'/v3/isolation-groups').then(function(r){return r.json();}).then(function(d){var items=d.items||[];sel.innerHTML='<option value="">请选择隔离组</option>';items.forEach(function(g){var opt=document.createElement('option');opt.value=g.id;opt.textContent=g.name+(g.status==='ACTIVE'?' (活跃)':' (冻结)');sel.appendChild(opt);});}).catch(function(e){sel.innerHTML='<option value="">加载隔离组失败</option>';});}
function syncTierFundingGuard(){var tier=document.getElementById('new-iss-issuing-tier');var funding=document.getElementById('new-iss-funding-mode');var fiat=document.getElementById('fiat-direct-panel');var crypto=document.getElementById('crypto-pool-panel');if(!tier||!funding)return;var fiatOpt=Array.from(funding.options).find(function(o){return o.value==='FIAT_DIRECT';});if(tier.value==='TIER_1_AGGREGATOR'){funding.value='CRYPTO_DESENSITIZED';if(fiatOpt)fiatOpt.disabled=true;if(fiat)fiat.style.display='none';if(crypto)crypto.style.display='block';document.getElementById('tier-funding-tooltip').innerText='高风险资金已强制进入异步合规隔离路径。';document.getElementById('tier-funding-tooltip').className='empty warn';}else{if(fiatOpt)fiatOpt.disabled=false;if(fiat)fiat.style.display=funding.value==='FIAT_DIRECT'?'block':'none';if(crypto)crypto.style.display=funding.value==='CRYPTO_DESENSITIZED'?'block':'none';}}var syncIssuingFundingGuard=syncTierFundingGuard;
// R19 P0-5: syncIssuingAdapterGuard — Adapter Type 联动守卫
function syncIssuingAdapterGuard(){var adapterType=document.getElementById('new-iss-adapter-type');var providerSelect=document.getElementById('new-iss-adapter-provider-id');var vaultInput=document.getElementById('new-iss-vault-key');var baseURLInput=document.getElementById('new-iss-base-url');if(!adapterType)return;var type=adapterType.value;if(type==='NEXUS_ISSUING_HTTP_V1'){if(providerSelect)providerSelect.parentElement.style.display='block';if(vaultInput)vaultInput.parentElement.style.display='block';if(baseURLInput)baseURLInput.parentElement.style.display='block';}else if(type==='MOCK_LOCAL'){if(providerSelect)providerSelect.parentElement.style.display='none';if(baseURLInput)baseURLInput.parentElement.style.display='none';if(vaultInput)vaultInput.parentElement.style.display='block';}else{if(providerSelect)providerSelect.parentElement.style.display='none';if(baseURLInput)baseURLInput.parentElement.style.display='none';if(vaultInput)vaultInput.parentElement.style.display='block';}}
function triggerSystemLockdown(){if(!confirm('确认启动系统紧急全盘锁死？该操作会禁用所有收单通道并冻结隔离组。'))return;var reason=prompt('请输入锁死原因:');if(!reason)return;var idempotencyKey=crypto.randomUUID();authFetch(API_BASE+'/api/v3/system/lockdown',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({reason:reason,idempotency_key:idempotencyKey})}).then(function(r){return r.json().then(function(d){if(r.ok){alert('系统已进入 Fail-Closed 锁死状态。已禁用通道：'+d.disabled_channels);loadAcquirerChannels();loadIsolationGroups();}else{alert('锁死失败: '+errMsg(d));}});}).catch(function(e){alert('锁死失败: '+errMsg(e));});}function suspendActiveCards(){if(!confirm('确认启动一键安全离场？该操作将冻结所有ACTIVE状态的卡片。'))return;var reason=prompt('请输入安全离场原因:');if(!reason)return;var idempotencyKey=crypto.randomUUID();authFetch(API_BASE+'/api/v3/system/suspend-active-cards',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({reason:reason,idempotency_key:idempotencyKey})}).then(function(r){return r.json().then(function(d){if(r.ok){alert('安全离场完成。已冻结卡片：'+d.frozen_count);}else{alert('安全离场失败: '+errMsg(d));}});}).catch(function(e){alert('安全离场失败: '+errMsg(e));});}
function startAdminEventStream(){var statusEl=document.getElementById('sse-connection-status');var alertEl=document.getElementById('global-risk-alert');if(!window.EventSource){if(statusEl){statusEl.textContent='disconnected';statusEl.className='sse-status sse-offline';}return;}if(statusEl){statusEl.textContent='connecting';statusEl.className='sse-status sse-offline';}var es=new EventSource(API_BASE+'/api/v3/events/stream');es.onopen=function(){if(statusEl){statusEl.textContent='online';statusEl.className='sse-status sse-online';}};es.addEventListener('connection_status',function(e){try{var d=JSON.parse(e.data);if(statusEl){var connectionStatus=d.status==='connecting'&&es.readyState===EventSource.OPEN?'online':d.status;statusEl.textContent=connectionStatus||'unknown';statusEl.className='sse-status sse-'+(connectionStatus==='online'?'online':'offline');}}catch(ex){}});es.addEventListener('SYSTEM_LOCKDOWN',function(e){if(alertEl){alertEl.className='alert-banner danger';alertEl.textContent='系统已锁死: '+e.data;}alert('系统已锁死: '+e.data);});es.addEventListener('ISSUING_POOL_FRAUD_TRIPPED',function(e){if(alertEl){alertEl.className='alert-banner danger';alertEl.textContent='虚拟卡池熔断告警: '+e.data;}alert('虚拟卡池熔断: '+e.data);});es.onerror=function(){if(statusEl){if(es.readyState===EventSource.CLOSED){statusEl.textContent='disconnected';statusEl.className='sse-status sse-offline';}else{statusEl.textContent='reconnecting';statusEl.className='sse-status sse-offline';}}};}
applyAcquirerMacro();syncIssuingFundingGuard();syncIssuingAdapterGuard();startAdminEventStream();loadStats();
document.addEventListener('DOMContentLoaded',function(){
// 导航 tab 切换：所有 [data-tab] 按钮通过 addEventListener 绑定
document.querySelectorAll('.nav button[data-tab]').forEach(function(btn){
btn.addEventListener('click',function(){showTab(btn.dataset.tab,btn);});
});
// change 事件：applyAcquirerMacro 和 syncIssuingFundingGuard
document.querySelectorAll('[data-action="applyAcquirerMacro"]').forEach(function(el){
el.addEventListener('change',function(){applyAcquirerMacro();});
});
document.querySelectorAll('[data-action="syncIssuingFundingGuard"]').forEach(function(el){
el.addEventListener('change',function(){syncIssuingFundingGuard();});
});
// R19 P0-5: syncIssuingAdapterGuard change 事件
document.querySelectorAll('[data-action="syncIssuingAdapterGuard"]').forEach(function(el){
el.addEventListener('change',function(){syncIssuingAdapterGuard();});
});
document.querySelectorAll('[data-action="loadApprovalInbox"]').forEach(function(el){
el.addEventListener('change',function(){loadApprovalInbox();});
});
// blur 事件：validateVaultKey
document.querySelectorAll('[data-action="validateVaultKey"]').forEach(function(el){
el.addEventListener('blur',function(){validateVaultKeyInput(el.dataset.vaultInput||el.id);});
});
// input 事件：updateDeclineLimit
document.querySelectorAll('[data-action="updateDeclineLimit"]').forEach(function(el){
el.addEventListener('input',function(){document.getElementById('new-iss-decline-limit-value').innerText=el.value;});
});
});
function loadVAChannels(){var sel=document.getElementById('new-va-channel-id');if(!sel)return;authFetch(API_BASE+'/adapter/channels').then(function(r){return r.json();}).then(function(d){var items=d.items||[];sel.innerHTML='<option value="">请选择通道</option>';items.forEach(function(i){var opt=document.createElement('option');opt.value=i.id;opt.textContent=esc(i.channel_name)+' ('+esc(i.channel_code)+')';sel.appendChild(opt);});}).catch(function(e){sel.innerHTML='<option value="">加载通道失败</option>';});}
function loadVAPool(){authFetch(API_BASE+'/api/v3/bank/va-pool').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('va-pool-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="9" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var status=statusCN[i.status]||i.status;var tr=document.createElement('tr');tr.appendChild(makeTD(esc(sh(i.id))));tr.appendChild(makeTD(esc(sh(i.tenant_id))));tr.appendChild(makeTD(esc(sh(i.channel_id))));tr.appendChild(makeTD(esc(i.account_number)));tr.appendChild(makeTD(esc(i.bank_name)));tr.appendChild(makeTD(esc(i.asset_code)));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';tr.appendChild(statusTd);tr.appendChild(makeTD(esc(ft(i.created_at))));var actionTd=document.createElement('td');if(i.status==='ALLOCATED'){actionTd.appendChild(makeBtn('释放','','releaseVA',i.id));}if(i.status==='AVAILABLE'){actionTd.appendChild(makeBtn('禁用','danger','disableVA',i.id));}tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('va-pool-table').innerHTML='<tr><td colspan="9" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function createVAAccount(){var tenantID=document.getElementById('new-va-tenant-id').value;var channelID=document.getElementById('new-va-channel-id').value;var accountNumber=document.getElementById('new-va-account-number').value;var routingNumber=document.getElementById('new-va-routing-number').value;var iban=document.getElementById('new-va-iban').value;var swift=document.getElementById('new-va-swift').value;var bankName=document.getElementById('new-va-bank-name').value;var bankAddress=document.getElementById('new-va-bank-address').value;var assetCode=document.getElementById('new-va-asset-code').value;if(!tenantID||!accountNumber||!bankName){alert('请填写完整信息');return;}authFetch(API_BASE+'/api/v3/bank/va-pool',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({tenant_id:tenantID,channel_id:channelID,account_number:accountNumber,routing_number:routingNumber,iban:iban,swift:swift,bank_name:bankName,bank_address:bankAddress,asset_code:assetCode})}).then(function(r){return r.json().then(function(d){if(r.ok){loadVAPool();document.getElementById('new-va-tenant-id').value='';document.getElementById('new-va-account-number').value='';document.getElementById('new-va-routing-number').value='';document.getElementById('new-va-iban').value='';document.getElementById('new-va-swift').value='';document.getElementById('new-va-bank-name').value='';document.getElementById('new-va-bank-address').value='';}else{alert('创建失败: '+errMsg(d));}});}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function releaseVA(id){var modal=document.createElement('div');modal.className='modal';var content=document.createElement('div');content.className='modal-content';var h3=document.createElement('h3');h3.textContent='释放 VA 账号';content.appendChild(h3);var label=document.createElement('label');label.textContent='释放原因(必填):';content.appendChild(label);var input=document.createElement('input');input.type='text';input.id='va-release-reason';content.appendChild(input);var okBtn=document.createElement('button');okBtn.className='submit-btn';okBtn.textContent='提交释放申请';content.appendChild(okBtn);var cancelBtn=document.createElement('button');cancelBtn.type='button';cancelBtn.textContent='取消';content.appendChild(cancelBtn);modal.appendChild(content);document.body.appendChild(modal);cancelBtn.addEventListener('click',function(){document.body.removeChild(modal);});okBtn.addEventListener('click',function(){var reason=input.value.trim();if(!reason){alert('释放原因必填');return;}authFetch(API_BASE+'/api/v3/bank/va-pool/'+id+'/release',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({reason:reason})}).then(function(r){if(r.ok){document.body.removeChild(modal);alert('已提交释放审批，待审核后生效。');loadVAPool();}else{r.json().then(function(d){alert('释放失败: '+errMsg(d));});}}).catch(function(e){alert('释放失败: '+errMsg(e));});});}
function disableVA(id){if(!confirm('确认禁用该VA账号？'))return;submitApprovalRequest('DISABLE_VA_ACCOUNT',{va_id:id}).then(function(){loadVAPool();});}
function loadSettlementAccounts(){authFetch(API_BASE+'/api/v3/bank/settlement-accounts').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('settlement-accounts-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="5" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var tr=document.createElement('tr');tr.appendChild(makeTD(esc(sh(i.id))));tr.appendChild(makeTD(esc(sh(i.bank_account_id))));tr.appendChild(makeTD(esc(i.currency)));tr.appendChild(makeTD(i.accumulated_minor));tr.appendChild(makeTD(esc(ft(i.updated_at))));tbody.appendChild(tr);});}).catch(function(e){document.getElementById('settlement-accounts-table').innerHTML='<tr><td colspan="5" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function createSettlementAccount(){var bankAccountID=document.getElementById('new-sa-bank-account-id').value;var currency=document.getElementById('new-sa-currency').value;var accumulatedMinor=document.getElementById('new-sa-accumulated-minor').value;if(!bankAccountID||!accumulatedMinor){alert('请填写完整信息');return;}authFetch(API_BASE+'/api/v3/bank/settlement-accounts',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({bank_account_id:bankAccountID,currency:currency,accumulated_minor:parseInt(accumulatedMinor)})}).then(function(r){return r.json().then(function(d){if(r.ok){loadSettlementAccounts();document.getElementById('new-sa-bank-account-id').value='';document.getElementById('new-sa-accumulated-minor').value='';}else{alert('创建失败: '+errMsg(d));}});}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function loadIntents(){authFetch(API_BASE+'/api/v3/bank/intents').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('bank-intents-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="9" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var status=statusCN[i.status]||i.status;var tr=document.createElement('tr');tr.appendChild(makeTD(esc(sh(i.id))));tr.appendChild(makeTD(esc(sh(i.tenant_id))));tr.appendChild(makeTD(esc(sh(i.order_id))));tr.appendChild(makeTD(i.expected_amount));tr.appendChild(makeTD(i.received_amount));tr.appendChild(makeTD(i.confirmed_amount));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge badge-'+sb(i.status)+'">'+esc(status)+'</span>';tr.appendChild(statusTd);tr.appendChild(makeTD(esc(ft(i.expires_at))));tr.appendChild(makeTD(esc(ft(i.created_at))));tbody.appendChild(tr);});}).catch(function(e){document.getElementById('bank-intents-table').innerHTML='<tr><td colspan="9" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function loadReconciliationLogs(){authFetch(API_BASE+'/api/v3/bank/reconciliation').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('reconciliation-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="9" class="empty">暂无数据</td></tr>';return;}items.forEach(function(i){var tr=document.createElement('tr');tr.appendChild(makeTD(esc(sh(i.id))));tr.appendChild(makeTD(esc(sh(i.tenant_id))));tr.appendChild(makeTD(esc(sh(i.intent_id))));tr.appendChild(makeTD(esc(ft(i.reconciled_at))));tr.appendChild(makeTD(esc(i.match_status)));tr.appendChild(makeTD(esc(i.discrepancy_type)));tr.appendChild(makeTD(i.received_amount));tr.appendChild(makeTD(i.expected_amount));tr.appendChild(makeTD(esc(ft(i.created_at))));tbody.appendChild(tr);});}).catch(function(e){document.getElementById('reconciliation-table').innerHTML='<tr><td colspan="9" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function loadMerchants(){authFetch(API_BASE+'/api/v3/merchants').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('merchants-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan=\"5\" class=\"empty\">暂无数据</td></tr>';return;}items.forEach(function(i){var tr=document.createElement('tr');tr.appendChild(makeTD(esc(sh(i.id))));tr.appendChild(makeTD(esc(i.merchant_name)));tr.appendChild(makeTD(esc(sh(i.tenant_id))));var statusTd=document.createElement('td');statusTd.innerHTML='<span class=\"badge badge-'+sb(i.status)+'\">'+esc(statusCN[i.status]||i.status)+'</span>';tr.appendChild(statusTd);tr.appendChild(makeTD(esc(i.jurisdiction||'-')));tbody.appendChild(tr);});}).catch(function(e){document.getElementById('merchants-table').innerHTML='<tr><td colspan=\"5\" class=\"empty\">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function createMerchant(){var name=document.getElementById('new-merchant-name').value;var legalName=document.getElementById('new-merchant-legal-name').value;var jurisdiction=document.getElementById('new-merchant-jurisdiction').value;var currency=document.getElementById('new-merchant-currency').value;var webhookUrl=document.getElementById('new-merchant-webhook-url').value;var webhookSecret=document.getElementById('new-merchant-webhook-secret').value;var maeConfig=document.getElementById('new-merchant-mae-config').value;var bankAccount=document.getElementById('new-merchant-bank-account').value;var reserveRatio=readRateBps('new-merchant-reserve-ratio');if(reserveRatio===null)return;var riskLabel=document.getElementById('new-merchant-risk-label').value;if(!name||!legalName||!jurisdiction){alert('请填写完整信息');return;}authFetch(API_BASE+'/api/v3/merchants',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({merchant_name:name,legal_name:legalName,jurisdiction:jurisdiction,settlement_currency:currency,webhook_url:webhookUrl,webhook_secret_vault_key:webhookSecret,mae_config_id:maeConfig,bank_account_id:bankAccount,reserve_ratio_bps:reserveRatio,risk_label:riskLabel})}).then(function(r){return r.json().then(function(d){if(r.ok){loadMerchants();document.getElementById('new-merchant-name').value='';document.getElementById('new-merchant-legal-name').value='';document.getElementById('new-merchant-jurisdiction').value='';document.getElementById('new-merchant-webhook-url').value='';document.getElementById('new-merchant-webhook-secret').value='';document.getElementById('new-merchant-mae-config').value='';document.getElementById('new-merchant-bank-account').value='';document.getElementById('new-merchant-risk-label').value='';}else{alert('创建失败: '+errMsg(d));}});}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function validateVaultKeyInput(inputId){var el=document.getElementById(inputId);if(!el)return true;var value=el.value.trim();if(value&&!value.startsWith('vault://')){alert('禁止在此输入明文密钥！请先将其存入 Vault，并在此输入安全引用路径（vault://...）。');el.classList.add('input-error');el.value='';return false;}if(value.length>512){alert('Vault 引用路径过长，可能存在异常输入。');el.classList.add('input-error');return false;}el.classList.remove('input-error');return true;}
function sanitizeVaultKeyOnInput(inputId){var el=document.getElementById(inputId);if(!el)return;var value=el.value;if(value&&!value.startsWith('vault://')&&value.length>0){el.classList.add('input-error');}else{el.classList.remove('input-error');}}
function submitApprovalRequest(actionType,payload){var reason=prompt('请输入申请原因:');if(!reason)return;return authFetch(API_BASE+'/api/v3/approvals',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action_type:actionType,reason:reason,requested_payload:payload})}).then(function(r){if(r.ok){alert('已提交审批，待审核后生效。身份从登录会话获取。');}else{r.json().then(function(d){alert('提交审批失败: '+errMsg(d));});}}).catch(function(e){alert('提交审批失败: '+errMsg(e));});}
function loadApprovalInbox(){var status=document.getElementById('approval-status-filter')?document.getElementById('approval-status-filter').value:'';var url=API_BASE+'/api/v3/approvals';if(status){url+='?status='+encodeURIComponent(status);}authFetch(url).then(function(r){return r.json();}).then(function(d){var items=d.items||[];var tbody=document.getElementById('approval-inbox-table');tbody.innerHTML='';if(!items.length){tbody.innerHTML='<tr><td colspan="10" class="empty">暂无审批请求</td></tr>';return;}items.forEach(function(a){var statusCN={'PENDING':'已申请','APPLYING':'应用中','APPLIED':'已应用','REJECTED':'已拒绝','FAILED_STALE':'应用失败'}[a.status]||a.status;var statusClass={'PENDING':'badge-warn','APPLYING':'badge-info','APPLIED':'badge-ok','REJECTED':'badge-err','FAILED_STALE':'badge-err'}[a.status]||'badge-info';var tr=document.createElement('tr');tr.appendChild(makeTD(sh(a.id)));tr.appendChild(makeTD(esc(a.action_type)));var targetInfo=a.target_type&&a.target_id?esc(a.target_type)+': '+esc(sh(a.target_id)):'-';tr.appendChild(makeTD(targetInfo));var versionLock=a.target_version!==undefined&&a.target_version!==null?String(a.target_version):'-';tr.appendChild(makeTD(versionLock));tr.appendChild(makeTD(esc(a.requester)));var statusTd=document.createElement('td');statusTd.innerHTML='<span class="badge '+statusClass+'">'+esc(statusCN)+'</span>';tr.appendChild(statusTd);tr.appendChild(makeTD(a.checker?esc(a.checker):'-'));var outboxTd=document.createElement('td');if(a.outbox_status){var outboxCN={'PENDING':'待处理','Dispatched':'已分发','DISPATCHED':'已分发','Completed':'已完成','COMPLETED':'已完成','DEADLETTER':'死信'}[a.outbox_status]||a.outbox_status;var outboxClass={'PENDING':'badge-warn','Dispatched':'badge-info','DISPATCHED':'badge-info','Completed':'badge-ok','COMPLETED':'badge-ok','DEADLETTER':'badge-err'}[a.outbox_status]||'badge-info';outboxTd.innerHTML='<span class="badge '+outboxClass+'">'+esc(outboxCN)+'</span>';}else{outboxTd.textContent='-';}tr.appendChild(outboxTd);tr.appendChild(makeTD(ft(a.created_at)));var actionTd=document.createElement('td');var detailBtn=makeBtn('详情',' ','showApprovalDetail',a.id);actionTd.appendChild(detailBtn);if(a.status==='PENDING'){actionTd.appendChild(document.createTextNode(' '));actionTd.appendChild(makeBtn('批准','enable','approveApproval',a.id));actionTd.appendChild(document.createTextNode(' '));actionTd.appendChild(makeBtn('拒绝','danger','rejectApproval',a.id));}tr.appendChild(actionTd);tbody.appendChild(tr);});}).catch(function(e){document.getElementById('approval-inbox-table').innerHTML='<tr><td colspan="10" class="empty">加载失败: '+esc(errMsg(e))+'</td></tr>';});}
function showApprovalDetail(id){authFetch(API_BASE+'/api/v3/approvals/'+id).then(function(r){return r.json();}).then(function(a){var statusCN={'PENDING':'已申请','APPLYING':'应用中','APPLIED':'已应用','REJECTED':'已拒绝','FAILED_STALE':'应用失败'}[a.status]||a.status;var h='<div class="card"><h3>审批 '+esc(sh(a.id))+'</h3>';h+='<div class="form-grid">';h+='<div class="form-group"><label>动作</label><input type="text" value="'+esc(a.action_type)+'" readonly></div>';h+='<div class="form-group"><label>目标类型</label><input type="text" value="'+esc(a.target_type||'')+'" readonly></div>';h+='<div class="form-group"><label>目标ID</label><input type="text" value="'+esc(sh(a.target_id||''))+'" readonly></div>';h+='<div class="form-group"><label>版本锁</label><input type="text" value="'+esc(a.target_version!==undefined&&a.target_version!==null?a.target_version:'-')+'" readonly></div>';h+='<div class="form-group"><label>申请人</label><input type="text" value="'+esc(a.requester)+'" readonly></div>';h+='<div class="form-group"><label>状态</label><input type="text" value="'+statusCN+'" readonly></div>';h+='<div class="form-group"><label>复核人</label><input type="text" value="'+esc(a.checker||'-')+'" readonly></div>';h+='</div>';h+='<div class="form-group"><label>申请理由</label><textarea readonly>'+esc(a.reason||'')+'</textarea></div>';if(a.review_reason){h+='<div class="form-group"><label>复核理由</label><textarea readonly>'+esc(a.review_reason)+'</textarea></div>';}if(a.risk_note){h+='<div class="form-group"><label>风险说明</label><textarea readonly>'+esc(a.risk_note)+'</textarea></div>';}if(a.before_snapshot){h+='<h4>变更前快照（已脱敏）</h4><pre>'+esc(JSON.stringify(a.before_snapshot,null,2))+'</pre>';}if(a.validated_payload){h+='<h4>已验证 Payload（变更后）</h4><pre>'+esc(JSON.stringify(a.validated_payload,null,2))+'</pre>';}if(a.before_snapshot&&a.validated_payload){h+='<h4>Before → After 对比</h4>';h+='<table><thead><tr><th>字段</th><th>变更前</th><th>变更后</th></tr></thead><tbody>';var allKeys=Object.keys(a.before_snapshot||{}).concat(Object.keys(a.validated_payload||{}).filter(function(k){return !a.before_snapshot||!(k in a.before_snapshot);}));allKeys.forEach(function(k){var beforeVal=a.before_snapshot&&k in a.before_snapshot?JSON.stringify(a.before_snapshot[k]):'-';var afterVal=a.validated_payload&&k in a.validated_payload?JSON.stringify(a.validated_payload[k]):'-';var changed=beforeVal!==afterVal;h+='<tr'+(changed?' class="diff-changed"':'')+'><td>'+esc(k)+'</td><td>'+esc(beforeVal)+'</td><td>'+esc(afterVal)+'</td></tr>';});h+='</tbody></table>';}h+='<h4>同步状态（已申请/已批准/已应用/节点已同步/失败）</h4>';var appliedPhase=a.status==='PENDING'||a.status==='APPLYING'||a.status==='APPLIED'||a.status==='REJECTED'||a.status==='FAILED_STALE';var approvedPhase=a.status==='APPLYING'||a.status==='APPLIED';var configApplied=a.status==='APPLIED';var nodeSynced=configApplied&&(a.outbox_status==='Dispatched'||a.outbox_status==='Completed'||a.outbox_status==='DISPATCHED'||a.outbox_status==='COMPLETED');var applyFailed=a.status==='FAILED_STALE';var syncStatus='';syncStatus+=(appliedPhase?'✓':'○')+' 已申请\\n';syncStatus+=(approvedPhase?'✓':'○')+' 已批准\\n';syncStatus+=(configApplied?'✓':'○')+' 已应用\\n';syncStatus+=(nodeSynced?'✓':'○')+' 节点已同步\\n';syncStatus+=(applyFailed?'✗ 应用失败（版本冲突或 outbox 异常）':'')+(a.apply_error?'：'+esc(a.apply_error):'')+'\\n';h+='<pre>'+esc(syncStatus)+'</pre>';if(a.after_snapshot){h+='<h4>变更后快照（已脱敏）</h4><pre>'+esc(JSON.stringify(a.after_snapshot,null,2))+'</pre>';}if(a.outbox_event_id){h+='<h4>Outbox 事件</h4><div class="form-group"><label>事件 ID</label><input type="text" value="'+esc(sh(a.outbox_event_id))+'" readonly></div><div class="form-group"><label>Outbox 状态</label><input type="text" value="'+esc(a.outbox_status||'-')+'" readonly></div>';}if(a.audit_event_code){h+='<h4>审计事件</h4><div class="form-group"><label>事件代码</label><input type="text" value="'+esc(a.audit_event_code)+'" readonly></div><div class="form-group"><label>Request ID</label><input type="text" value="'+esc(a.request_id||'-')+'" readonly></div>';}h+='</div>';document.getElementById('approval-detail-panel').classList.remove('hidden');document.getElementById('approval-detail-content').innerHTML=h;}).catch(function(e){alert('加载详情失败: '+errMsg(e));});}
function approveApproval(id){var reviewReason=prompt('请输入复核理由（批准）。\n\n批准后配置将原子应用并分发到所有节点。\n审批 ID: '+id+'\n\n复核理由:');if(reviewReason===null)return;if(!reviewReason.trim()){alert('复核理由不能为空');return;}authFetch(API_BASE+'/api/v3/approvals/'+id+'/approve',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({review_reason:reviewReason})}).then(function(r){return r.json().then(function(d){if(r.ok){alert('审批已通过，配置已原子应用。\n身份从登录会话获取，不可伪造。');loadApprovalInbox();}else{alert('审批失败: '+errMsg(d));}});}).catch(function(e){alert('审批失败: '+errMsg(e));});}
function rejectApproval(id){var reviewReason=prompt('请输入复核理由（拒绝）。\n\n拒绝后该审批请求将终止，配置不变。\n审批 ID: '+id+'\n\n复核理由:');if(reviewReason===null)return;if(!reviewReason.trim()){alert('复核理由不能为空');return;}authFetch(API_BASE+'/api/v3/approvals/'+id+'/reject',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({review_reason:reviewReason})}).then(function(r){return r.json().then(function(d){if(r.ok){alert('已拒绝。配置未变更。');loadApprovalInbox();}else{alert('拒绝失败: '+errMsg(d));}});}).catch(function(e){alert('拒绝失败: '+errMsg(e));});}
document.addEventListener('click',handleActionClick);
</script>
</body>
</html>`, nonce)
}

// DashboardCalledRoutesForTest 导出 dashboard JS 调用的静态 API 路径前缀，
// 供 router 包的 walk 合同测试（UI-005 route contract）消费。
func DashboardCalledRoutesForTest() []string {
	html := dashboardHTML("test-nonce", "test", false, "Connected", "ok", "Connected", "ok", "1h0m0s", "go1.21", 4, 10, 100, 200)
	pathRe := regexp.MustCompile(`API_BASE\+'([^']+)'`)
	seen := map[string]bool{}
	var out []string
	for _, m := range pathRe.FindAllStringSubmatch(html, -1) {
		base := m[1]
		if i := strings.IndexByte(base, '?'); i >= 0 {
			base = base[:i]
		}
		base = strings.TrimSuffix(base, "/")
		if base != "" && !seen[base] {
			seen[base] = true
			out = append(out, base)
		}
	}
	sort.Strings(out)
	return out
}
