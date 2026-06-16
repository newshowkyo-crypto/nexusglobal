package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

var startTime = time.Now()

func DashboardHandler(pool *pgxpool.Pool, rdb *redis.Client, env string) http.HandlerFunc {
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

func AdminDashboardHTML(pool *pgxpool.Pool, rdb *redis.Client, env string) http.HandlerFunc {
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

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(dashboardHTML(env, dbStatus, dbClass, redisStatus, redisClass, time.Since(startTime).Round(time.Second).String(), runtime.Version(), runtime.NumCPU(), runtime.NumGoroutine(), int(mem.Alloc/1024/1024), int(mem.Sys/1024/1024))))
	}
}

func dashboardHTML(env, dbStatus, dbClass, redisStatus, redisClass, uptime, goVer string, cpu, goroutines, memAlloc, memSys int) string {
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>NexusGlobal MAMR 管理后台</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#0f172a;color:#e2e8f0;min-height:100vh}
.nav{display:flex;align-items:center;gap:4px;padding:12px 20px;background:#1e293b;border-bottom:1px solid #334155;flex-wrap:wrap}
.logo{font-weight:700;font-size:16px;color:#38bdf8;margin-right:16px;white-space:nowrap}
.nav button{background:#1e293b;color:#94a3b8;border:1px solid #334155;padding:8px 14px;border-radius:6px;cursor:pointer;font-size:13px;transition:all .2s}
.nav button:hover{background:#334155;color:#e2e8f0}
.nav button.active{background:#38bdf8;color:#0f172a;font-weight:600}
.main{padding:20px;max-width:1400px;margin:0 auto}
.tab{display:none}.tab.active{display:block}
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
.badge-info{background:#172554;color:#60a5fa}
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
</style>
</head>
<body>
<div class="nav"><span class="logo">NexusGlobal MAMR</span>
<button class="active" onclick="showTab('overview',this)">总览</button>
<button onclick="showTab('universal-adapter',this)">通用适配器</button>
<button onclick="showTab('corporate-entity',this)">法人实体</button>
<button onclick="showTab('asset-bridge',this)">资产桥配置</button>
<button onclick="showTab('wallet-policy',this)">钱包白名单</button>
<button onclick="showTab('risk-ops',this)">风控运维</button>
<button onclick="showTab('channel-risk',this)">通道风控</button>
<button onclick="showTab('merchant-reserve',this)">商户准备金</button>
<button onclick="showTab('fee-allocation',this)">费用摊销</button>
<button onclick="showTab('pipeline-isolation',this)">管道隔离</button>
<button onclick="showTab('compliance-rfi',this)">合规资料包</button>
<button onclick="showTab('acquirer-channels',this)">收单通道Tier</button>
<button onclick="showTab('issuing-pools',this)">发卡卡池Tier</button>
</div>
<div class="main">
<div id="tab-overview" class="tab active"><div class="grid">
<div class="stat"><div class="label">系统状态</div><div class="value"><span class="status-dot dot-ok"></span>运行中</div><div class="sub">运行时间: ` + uptime + `</div></div>
<div class="stat"><div class="label">PostgreSQL 数据库</div><div class="value"><span class="status-dot dot-` + dbClass + `"></span>` + dbStatus + `</div><div class="sub">主数据存储</div></div>
<div class="stat"><div class="label">Redis 缓存</div><div class="value"><span class="status-dot dot-` + redisClass + `"></span>` + redisStatus + `</div><div class="sub">消息队列 + 缓存</div></div>
<div class="stat"><div class="label">运行环境</div><div class="value">` + env + `</div><div class="sub">Go ` + goVer + `</div></div>
<div class="stat"><div class="label">内存使用</div><div class="value">` + fmt.Sprint(memAlloc) + ` MB</div><div class="sub">已分配 / 系统总计</div></div>
<div class="stat"><div class="label">协程数</div><div class="value">` + fmt.Sprint(goroutines) + `</div><div class="sub">活跃 goroutine</div></div>
</div><div class="section"><h2>业务统计 <button class="refresh-btn" onclick="loadStats()">刷新</button></h2><div id="stats-content" class="loading">加载中...</div></div></div>
<div id="tab-universal-adapter" class="tab">
<div class="section"><h2>适配器提供商 <button class="refresh-btn" onclick="loadAdapterProviders()">刷新</button></h2>
<div class="card">
<h3>新增提供商</h3>
<div class="form-grid">
<div class="form-group"><label>提供商代码</label><input type="text" id="new-provider-code" placeholder="如: stripe_sg"></div>
<div class="form-group"><label>提供商名称</label><input type="text" id="new-provider-name" placeholder="如: Stripe 新加坡"></div>
<div class="form-group"><label>适配器类型</label><select id="new-provider-type"><option value="CARD_ACQUIRER">卡网收单</option><option value="BANK_COLLECTION">银行虚拟户入金</option><option value="CRYPTO_PAY">加密支付</option><option value="BANK_RAIL">银行通道</option><option value="EXCHANGE">交易所</option><option value="LP">流动性提供商</option><option value="ONCHAIN_WALLET">链上钱包</option></select></div>
</div>
<button class="submit-btn" onclick="createAdapterProvider()">创建提供商</button>
</div>
<table><thead><tr><th>提供商代码</th><th>名称</th><th>类型</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="adapter-providers-table"><tr><td colspan="6" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>适配器通道 <button class="refresh-btn" onclick="loadAdapterChannels()">刷新</button></h2>
<div class="card">
<h3>新增通道</h3>
<div class="form-grid">
<div class="form-group"><label>通道代码</label><input type="text" id="new-channel-code" placeholder="如: stripe_visa_sg"></div>
<div class="form-group"><label>通道名称</label><input type="text" id="new-channel-name" placeholder="如: Stripe Visa 新加坡"></div>
<div class="form-group"><label>提供商</label><select id="new-channel-provider"><option value="">加载中...</option></select></div>
<div class="form-group"><label>适配器类型</label><select id="new-channel-type"><option value="CARD_ACQUIRER">卡网收单</option><option value="BANK_COLLECTION">银行虚拟户入金</option><option value="CRYPTO_PAY">加密支付</option><option value="BANK_RAIL">银行通道</option></select></div>
<div class="form-group"><label>支持资产（逗号分隔）</label><input type="text" id="new-channel-assets" placeholder="如: USD,SGD,EUR"></div>
<div class="form-group"><label>支持网络（逗号分隔）</label><input type="text" id="new-channel-networks" placeholder="如: VISA,MASTERCARD"></div>
</div>
<button class="submit-btn" onclick="createAdapterChannel()">创建通道</button>
</div>
<table><thead><tr><th>通道代码</th><th>名称</th><th>提供商</th><th>类型</th><th>资产</th><th>网络</th><th>状态</th><th>操作</th></tr></thead><tbody id="adapter-channels-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-corporate-entity" class="tab">
<div class="section"><h2>法人实体 <button class="refresh-btn" onclick="loadEntities()">刷新</button></h2>
<div class="card">
<h3>新增实体</h3>
<div class="form-grid">
<div class="form-group"><label>租户 ID</label><input type="text" id="new-entity-tenant" placeholder="输入租户 ID"></div>
<div class="form-group"><label>实体名称</label><input type="text" id="new-entity-name" placeholder="如: Acme Corp"></div>
<div class="form-group"><label>法人名称</label><input type="text" id="new-entity-legal" placeholder="如: Acme Corporation Ltd."></div>
<div class="form-group"><label>国家</label><select id="new-entity-country"><option value="SG">新加坡 (SG)</option><option value="HK">香港 (HK)</option><option value="US">美国 (US)</option><option value="UK">英国 (UK)</option><option value="CN">中国 (CN)</option></select></div>
<div class="form-group"><label>司法辖区</label><input type="text" id="new-entity-jurisdiction" placeholder="如: 新加坡"></div>
</div>
<button class="submit-btn" onclick="createEntity()">创建实体</button>
</div>
<table><thead><tr><th>实体名称</th><th>法人名称</th><th>国家</th><th>司法辖区</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="entities-table"><tr><td colspan="7" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-asset-bridge" class="tab">
<div class="section"><h2>资产桥配置 <button class="refresh-btn" onclick="loadBridgeProfiles()">刷新</button></h2>
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
<button class="submit-btn" onclick="createBridgeProfile()">创建配置</button>
</div>
<table><thead><tr><th>名称</th><th>源资产</th><th>目标资产</th><th>缓冲%</th><th>滑点限制</th><th>批处理</th><th>执行</th><th>状态</th><th>操作</th></tr></thead><tbody id="bridge-profiles-table"><tr><td colspan="9" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-wallet-policy" class="tab">
<div class="section"><h2>钱包白名单 <button class="refresh-btn" onclick="loadWalletPolicies()">刷新</button></h2>
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
<button class="submit-btn" onclick="createWalletPolicy()">创建策略</button>
</div>
<table><thead><tr><th>策略代码</th><th>资产</th><th>网络</th><th>地址</th><th>类型</th><th>单笔限额</th><th>日限额</th><th>状态</th><th>操作</th></tr></thead><tbody id="wallet-policies-table"><tr><td colspan="9" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-risk-ops" class="tab">
<div class="section"><h2>风险事件 <button class="refresh-btn" onclick="loadRiskEvents()">刷新</button></h2>
<table><thead><tr><th>事件ID</th><th>事件类型</th><th>实体ID</th><th>通道ID</th><th>严重性</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="risk-events-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>风险冻结实体 <button class="refresh-btn" onclick="loadRiskHoldStatus()">刷新</button></h2>
<table><thead><tr><th>实体类型</th><th>实体ID</th><th>名称</th><th>状态</th><th>更新时间</th><th>操作</th></tr></thead><tbody id="risk-hold-table"><tr><td colspan="6" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>待处理任务 <button class="refresh-btn" onclick="loadPendingTasks()">刷新</button></h2>
<table><thead><tr><th>任务ID</th><th>事件类型</th><th>状态</th><th>聚合ID</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="pending-tasks-table"><tr><td colspan="6" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-channel-risk" class="tab">
<div class="section"><h2>通道风控配置 <button class="refresh-btn" onclick="loadChannelRiskConfig()">刷新</button></h2>
<div class="card">
<h3>新增风控配置</h3>
<div class="form-grid">
<div class="form-group"><label>通道代码</label><select id="new-risk-channel"><option value="">请选择通道</option></select></div>
<div class="form-group"><label>风控模式</label><select id="new-risk-mode"><option value="DEFAULT">标准模式</option><option value="STRICT">严格模式</option><option value="MANUAL_REVIEW">人工审核</option></select></div>
<div class="form-group"><label>拒付率阈值(bps)</label><input type="number" id="new-risk-chargeback" placeholder="100" value="100"></div>
<div class="form-group"><label>退款率阈值(bps)</label><input type="number" id="new-risk-refund" placeholder="500" value="500"></div>
<div class="form-group"><label>失败率阈值(bps)</label><input type="number" id="new-risk-failure" placeholder="3000" value="3000"></div>
</div>
<button class="submit-btn" onclick="createChannelRiskConfig()">创建配置</button>
</div>
<table><thead><tr><th>通道代码</th><th>风控模式</th><th>拒付阈值</th><th>退款阈值</th><th>失败阈值</th><th>状态</th><th>操作</th></tr></thead><tbody id="channel-risk-table"><tr><td colspan="7" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>风控覆盖记录 <button class="refresh-btn" onclick="loadRiskOverrides()">刷新</button></h2>
<table><thead><tr><th>ID</th><th>实体类型</th><th>实体ID</th><th>覆盖类型</th><th>原因</th><th>操作人</th><th>过期时间</th><th>创建时间</th></tr></thead><tbody id="risk-overrides-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-merchant-reserve" class="tab">
<div class="section"><h2>商户风险配置 <button class="refresh-btn" onclick="loadMerchantRiskProfiles()">刷新</button></h2>
<div class="card">
<h3>新增风险配置</h3>
<div class="form-grid">
<div class="form-group"><label>商户ID</label><select id="new-reserve-merchant"><option value="">请选择商户</option></select></div>
<div class="form-group"><label>滚动准备金率(bps)</label><input type="number" id="new-reserve-rolling" placeholder="1000" value="1000"></div>
<div class="form-group"><label>争议准备金率(bps)</label><input type="number" id="new-reserve-dispute" placeholder="500" value="500"></div>
<div class="form-group"><label>释放天数</label><input type="number" id="new-reserve-days" placeholder="30" value="30"></div>
</div>
<button class="submit-btn" onclick="createMerchantRiskProfile()">创建配置</button>
</div>
<table><thead><tr><th>商户ID</th><th>滚动准备金率</th><th>争议准备金率</th><th>释放天数</th><th>状态</th><th>操作</th></tr></thead><tbody id="merchant-risk-profiles-table"><tr><td colspan="6" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>准备金明细 <button class="refresh-btn" onclick="loadReserveEntries()">刷新</button></h2>
<table><thead><tr><th>ID</th><th>商户ID</th><th>交易ID</th><th>金额</th><th>类型</th><th>释放日期</th><th>状态</th></tr></thead><tbody id="reserve-entries-table"><tr><td colspan="7" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-fee-allocation" class="tab">
<div class="section"><h2>费用摊销记录 <button class="refresh-btn" onclick="loadFeeAllocations()">刷新</button></h2>
<table><thead><tr><th>ID</th><th>交易ID</th><th>商户ID</th><th>费用类型</th><th>金额</th><th>币种</th><th>状态</th><th>创建时间</th></tr></thead><tbody id="fee-allocations-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>费用统计 <button class="refresh-btn" onclick="loadFeeStats()">刷新</button></h2>
<div id="fee-stats-content" class="loading">加载中...</div></div>
</div>
<div id="tab-pipeline-isolation" class="tab">
<div class="section"><h2>管道隔离组 <button class="refresh-btn" onclick="loadIsolationGroups()">刷新</button></h2>
<div class="card">
<h3>新增隔离组</h3>
<div class="form-grid">
<div class="form-group"><label>组名称</label><input type="text" id="new-group-name" placeholder="如: Group-A"></div>
<div class="form-group"><label>描述</label><input type="text" id="new-group-desc" placeholder="描述信息"></div>
</div>
<button class="submit-btn" onclick="createIsolationGroup()">创建组</button>
</div>
<table><thead><tr><th>组名称</th><th>描述</th><th>状态</th><th>冻结原因</th><th>冻结人</th><th>冻结时间</th><th>操作</th></tr></thead><tbody id="isolation-groups-table"><tr><td colspan="7" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>跨组操作记录</h2>
<table><thead><tr><th>时间</th><th>源组</th><th>目标组</th><th>操作类型</th><th>结果</th></tr></thead><tbody id="cross-group-operations-table"><tr><td colspan="5" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-compliance-rfi" class="tab">
<div class="section"><h2>合规资料包 <button class="refresh-btn" onclick="loadRFIDocuments()">刷新</button></h2>
<div class="card">
<h3>生成资料包</h3>
<div class="form-grid">
<div class="form-group"><label>订单ID</label><input type="text" id="new-rfi-order" placeholder="输入订单ID"></div>
<div class="form-group"><label>文档类型</label><select id="new-rfi-type"><option value="INVOICE">发票</option><option value="CONTRACT">合同</option><option value="SHIPPING">物流</option><option value="PROOF_OF_DELIVERY">履约证明</option></select></div>
</div>
<button class="submit-btn" onclick="generateRFIDocument()">生成文档</button>
</div>
<table><thead><tr><th>ID</th><th>订单ID</th><th>商户ID</th><th>类型</th><th>状态</th><th>源数据哈希</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="rfi-documents-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-acquirer-channels" class="tab">
<div class="section"><h2>收单通道 Tier 管理 <button class="refresh-btn" onclick="loadAcquirerChannels()">刷新</button></h2>
<div class="card">
<h3>新增收单通道</h3>
<div class="form-grid">
<div class="form-group"><label>实体ID</label><input type="text" id="new-acq-entity-id" placeholder="输入实体ID"></div>
<div class="form-group"><label>Provider Tier</label><select id="new-acq-provider-tier"><option value="TIER_1_HIGH_RISK">TIER_1_HIGH_RISK</option><option value="TIER_2_COMPLIANT">TIER_2_COMPLIANT</option></select></div>
<div class="form-group"><label>Provider Code</label><input type="text" id="new-acq-provider-code" placeholder="如: stripe"></div>
<div class="form-group"><label>Channel Code</label><input type="text" id="new-acq-channel-code" placeholder="如: stripe_visa"></div>
<div class="form-group"><label>Credential Vault Key</label><input type="text" id="new-acq-vault-key" placeholder="vault://..."></div>
<div class="form-group"><label>Strategy Mode</label><select id="new-acq-strategy-mode"><option value="DEFAULT">DEFAULT</option><option value="BURN_MODE">BURN_MODE</option></select></div>
</div>
<button class="submit-btn" onclick="createAcquirerChannel()">创建通道</button>
</div>
<table><thead><tr><th>ID</th><th>实体ID</th><th>Provider Tier</th><th>Provider Code</th><th>Channel Code</th><th>Strategy Mode</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="acquirer-channels-table"><tr><td colspan="9" class="loading">加载中...</td></tr></tbody></table></div>
</div>
<div id="tab-issuing-pools" class="tab">
<div class="section"><h2>发卡卡池 Tier 管理 <button class="refresh-btn" onclick="loadIssuingPools()">刷新</button></h2>
<div class="card">
<h3>新增发卡卡池</h3>
<div class="form-grid">
<div class="form-group"><label>实体ID</label><input type="text" id="new-iss-entity-id" placeholder="输入实体ID（可选）"></div>
<div class="form-group"><label>Issuing Tier</label><select id="new-iss-issuing-tier"><option value="TIER_1_AGGREGATOR">TIER_1_AGGREGATOR</option><option value="TIER_2_DIRECT">TIER_2_DIRECT</option></select></div>
<div class="form-group"><label>Funding Mode</label><select id="new-iss-funding-mode"><option value="CRYPTO_DESENSITIZED">CRYPTO_DESENSITIZED</option><option value="FIAT_DIRECT">FIAT_DIRECT</option></select></div>
<div class="form-group"><label>Provider Code</label><input type="text" id="new-iss-provider-code" placeholder="如: checkout_com"></div>
<div class="form-group"><label>Pool Code</label><input type="text" id="new-iss-pool-code" placeholder="如: usd_pool_001"></div>
<div class="form-group"><label>Credential Vault Key</label><input type="text" id="new-iss-vault-key" placeholder="vault://..."></div>
</div>
<button class="submit-btn" onclick="createIssuingPool()">创建卡池</button>
</div>
<table><thead><tr><th>ID</th><th>实体ID</th><th>Issuing Tier</th><th>Funding Mode</th><th>Provider Code</th><th>Pool Code</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody id="issuing-pools-table"><tr><td colspan="9" class="loading">加载中...</td></tr></tbody></table></div>
</div>
</div>
<script>
var API_BASE='/admin';var AUTH_TOKEN='';
var typeCN={'CARD_ACQUIRER':'卡网收单','BANK_COLLECTION':'银行虚拟户入金','CRYPTO_PAY':'加密支付','BANK_RAIL':'银行通道','EXCHANGE':'交易所','LP':'流动性提供商','ONCHAIN_WALLET':'链上钱包'};
var statusCN={'ACTIVE':'启用','DISABLED':'禁用','RISK_HOLD':'风险冻结'};
var destTypeCN={'TRANSIT':'过渡钱包','COLD_WALLET':'冷钱包','CUSTODY':'托管钱包'};
var batchingCN={'IMMEDIATE':'立即执行','ACCUMULATE':'累积批处理','SCHEDULED':'定时批处理'};
var execCN={'MARKET':'市价','LIMIT':'限价','SLICED':'分片执行'};
function showTab(name,btn){document.querySelectorAll('.tab').forEach(function(t){t.classList.remove('active');});document.querySelectorAll('.nav button').forEach(function(b){b.classList.remove('active');});document.getElementById('tab-'+name).classList.add('active');btn.classList.add('active');if(name==='overview')loadStats();if(name==='universal-adapter'){loadAdapterProviders();loadAdapterChannels();}if(name==='corporate-entity')loadEntities();if(name==='asset-bridge')loadBridgeProfiles();if(name==='wallet-policy')loadWalletPolicies();if(name==='risk-ops'){loadRiskEvents();loadRiskHoldStatus();loadPendingTasks();}if(name==='channel-risk'){loadChannelRiskConfig();loadOverrideAuditLogs();}if(name==='merchant-reserve'){loadMerchantRiskProfiles();loadRollingReserves();}if(name==='fee-allocation')loadFeeAllocations();if(name==='pipeline-isolation')loadIsolationGroups();if(name==='compliance-rfi')loadRFIDocuments();if(name==='acquirer-channels')loadAcquirerChannels();if(name==='issuing-pools')loadIssuingPools();}
function authFetch(url,opts){opts=opts||{};opts.headers=opts.headers||{};opts.headers['Authorization']=AUTH_TOKEN;return fetch(url,opts);}
function errMsg(d){if(d&&d.error&&d.error.message)return d.error.message;if(d&&d.message)return d.message;return '未知错误';}
function sh(id){return id?String(id).substring(0,8)+'...':'-';}
function ft(t){return t?String(t).substring(0,19).replace('T',' '):'-';}
function sb(s){if(!s)return'info';s=s.toUpperCase();if(s==='ACTIVE'||s==='CONFIRMED'||s==='RECONCILED'||s==='COMPLETED'||s==='SUBMITTED')return'ok';if(s==='FAILED'||s==='REJECTED'||s==='DEADLETTER')return'err';if(s==='COOLDOWN'||s==='RISK_HOLD'||s==='PENDING')return'warn';return'info';}
function loadStats(){authFetch(API_BASE+'/stats').then(function(r){return r.json();}).then(function(d){var h='<div class="grid">';h+='<div class="stat"><div class="label">通用适配器</div><div class="value">'+(d.adapter_provider_count||0)+'</div><div class="sub">个提供商</div></div>';h+='<div class="stat"><div class="label">通道配置</div><div class="value">'+(d.adapter_channel_count||0)+'</div><div class="sub">个通道</div></div>';h+='<div class="stat"><div class="label">法人实体</div><div class="value">'+(d.entity_count||0)+'</div><div class="sub">个实体</div></div>';h+='<div class="stat"><div class="label">桥接配置</div><div class="value">'+(d.bridge_profile_count||0)+'</div><div class="sub">个配置</div></div>';h+='<div class="stat"><div class="label">钱包策略</div><div class="value">'+(d.wallet_policy_count||0)+'</div><div class="sub">个策略</div></div>';h+='<div class="stat"><div class="label">待处理任务</div><div class="value">'+(d.outbox_pending||0)+'</div><div class="sub">条待处理</div></div>';h+='</div>';document.getElementById('stats-content').innerHTML=h;}).catch(function(){document.getElementById('stats-content').innerHTML='<div class="empty">加载失败（请设置 JWT Token）</div>';});}
function loadAdapterProviders(){authFetch(API_BASE+'/adapter/providers').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('adapter-providers-table').innerHTML='<tr><td colspan="6" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var status=statusCN[i.status]||i.status;var type=typeCN[i.adapter_type]||i.adapter_type;h+='<tr><td>'+i.provider_code+'</td><td>'+i.provider_name+'</td><td>'+type+'</td><td><span class="badge badge-'+sb(i.status)+'">'+status+'</span></td><td>'+ft(i.created_at)+'</td><td><button class="action-btn" onclick="deleteAdapterProvider(\''+i.id+'\')">删除</button></td></tr>';});document.getElementById('adapter-providers-table').innerHTML=h;}).catch(function(e){document.getElementById('adapter-providers-table').innerHTML='<tr><td colspan="6" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function createAdapterProvider(){var code=document.getElementById('new-provider-code').value;var name=document.getElementById('new-provider-name').value;var type=document.getElementById('new-provider-type').value;if(!code||!name){alert('请填写完整信息');return;}authFetch(API_BASE+'/adapter/providers',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({provider_code:code,provider_name:name,adapter_type:type,provider_config:{}})}).then(function(r){return r.json().then(function(d){if(r.ok){loadAdapterProviders();document.getElementById('new-provider-code').value='';document.getElementById('new-provider-name').value='';}else{alert('创建失败: '+errMsg(d));}});}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function deleteAdapterProvider(id){if(!confirm('确认删除该提供商？'))return;authFetch(API_BASE+'/adapter/providers/'+id,{method:'DELETE'}).then(function(r){if(r.ok){loadAdapterProviders();}else{r.json().then(function(d){alert('删除失败: '+errMsg(d));});}}).catch(function(e){alert('删除失败: '+errMsg(e));});}
function loadAdapterChannels(){authFetch(API_BASE+'/adapter/providers').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var opts='<option value="">请选择提供商</option>';items.forEach(function(i){opts+='<option value="'+i.provider_code+'">'+i.provider_name+' ('+i.provider_code+')</option>';});document.getElementById('new-channel-provider').innerHTML=opts;});authFetch(API_BASE+'/adapter/channels').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('adapter-channels-table').innerHTML='<tr><td colspan="8" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var status=statusCN[i.status]||i.status;var type=typeCN[i.adapter_type]||i.adapter_type;var assets=(i.supported_assets||[]).join(', ');var networks=(i.supported_networks||[]).join(', ');h+='<tr><td>'+i.channel_code+'</td><td>'+i.channel_name+'</td><td>'+sh(i.provider_id)+'</td><td>'+type+'</td><td>'+assets+'</td><td>'+networks+'</td><td><span class="badge badge-'+sb(i.status)+'">'+status+'</span></td><td><button class="action-btn" onclick="confirmDisableAdapterChannel(\''+i.id+'\')">禁用</button></td></tr>';});document.getElementById('adapter-channels-table').innerHTML=h;}).catch(function(e){document.getElementById('adapter-channels-table').innerHTML='<tr><td colspan="8" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function createAdapterChannel(){var code=document.getElementById('new-channel-code').value;var name=document.getElementById('new-channel-name').value;var provider=document.getElementById('new-channel-provider').value;var type=document.getElementById('new-channel-type').value;var assets=document.getElementById('new-channel-assets').value.split(',').map(function(s){return s.trim();}).filter(Boolean);var networks=document.getElementById('new-channel-networks').value.split(',').map(function(s){return s.trim();}).filter(Boolean);if(!code||!name||!provider){alert('请填写完整信息');return;}authFetch(API_BASE+'/adapter/channels',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({channel_code:code,channel_name:name,provider_code:provider,adapter_type:type,supported_assets:assets,supported_networks:networks,channel_config:{}})}).then(function(r){return r.json().then(function(d){if(r.ok){loadAdapterChannels();document.getElementById('new-channel-code').value='';document.getElementById('new-channel-name').value='';document.getElementById('new-channel-assets').value='';document.getElementById('new-channel-networks').value='';}else{alert('创建失败: '+errMsg(d));}});}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function confirmDisableAdapterChannel(id){if(!confirm('确认禁用该通道？禁用后该通道将无法处理交易。'))return;authFetch(API_BASE+'/adapter/channels/'+id+'/disable',{method:'POST'}).then(function(r){if(r.ok){loadAdapterChannels();}else{r.json().then(function(d){alert('禁用失败: '+errMsg(d));});}}).catch(function(e){alert('禁用失败: '+errMsg(e));});}
function loadEntities(){authFetch(API_BASE+'/entities').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('entities-table').innerHTML='<tr><td colspan="7" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var status=statusCN[i.status]||i.status;h+='<tr><td>'+i.name+'</td><td>'+i.legal_name+'</td><td>'+i.country+'</td><td>'+i.jurisdiction+'</td><td><span class="badge badge-'+sb(i.status)+'">'+status+'</span></td><td>'+ft(i.created_at)+'</td><td><button class="action-btn '+(i.status==='RISK_HOLD'?'enable':'')+'" onclick="'+(i.status==='RISK_HOLD'?'enableEntity':'confirmSetEntityRiskHold')+'(\''+i.id+'\')">'+(i.status==='RISK_HOLD'?'解除冻结':'风险冻结')+'</button></td></tr>';});document.getElementById('entities-table').innerHTML=h;}).catch(function(e){document.getElementById('entities-table').innerHTML='<tr><td colspan="7" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function createEntity(){var tenant=document.getElementById('new-entity-tenant').value;var name=document.getElementById('new-entity-name').value;var legal=document.getElementById('new-entity-legal').value;var country=document.getElementById('new-entity-country').value;var jurisdiction=document.getElementById('new-entity-jurisdiction').value;if(!tenant||!name||!legal){alert('请填写完整信息');return;}authFetch(API_BASE+'/entities',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({tenant_id:tenant,name:name,legal_name:legal,country:country,jurisdiction:jurisdiction})}).then(function(r){return r.json().then(function(d){if(r.ok){loadEntities();document.getElementById('new-entity-tenant').value='';document.getElementById('new-entity-name').value='';document.getElementById('new-entity-legal').value='';document.getElementById('new-entity-jurisdiction').value='';}else{alert('创建失败: '+errMsg(d));}});}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function confirmSetEntityRiskHold(id){if(!confirm('确认对该实体进行风险冻结？冻结后该实体的所有通道将停止处理交易。'))return;authFetch(API_BASE+'/entities/'+id+'/risk-hold',{method:'POST'}).then(function(r){if(r.ok){loadEntities();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}
function confirmDisableBridgeProfile(id){if(!confirm('确认禁用该桥接配置？禁用后将停止相关资产转换。'))return;authFetch(API_BASE+'/asset-bridge/profiles/'+id+'/disable',{method:'POST'}).then(function(r){if(r.ok){loadBridgeProfiles();}else{r.json().then(function(d){alert('禁用失败: '+errMsg(d));});}}).catch(function(e){alert('禁用失败: '+errMsg(e));});}
function enableEntity(id){authFetch(API_BASE+'/entities/'+id+'/enable',{method:'POST'}).then(function(r){if(r.ok){loadEntities();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}
function loadBridgeProfiles(){authFetch(API_BASE+'/asset-bridge/profiles').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('bridge-profiles-table').innerHTML='<tr><td colspan="9" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var batching=batchingCN[i.batching_policy]||i.batching_policy;var exec=execCN[i.execution_policy]||i.execution_policy;var status=i.enabled?'启用':'禁用';h+='<tr><td>'+i.name+'</td><td>'+i.source_asset+'</td><td>'+i.target_asset+'</td><td>'+i.buffer_percent+'%</td><td>'+i.max_slippage_bps+' bps</td><td>'+batching+'</td><td>'+exec+'</td><td><span class="badge badge-'+(i.enabled?'ok':'err')+'">'+status+'</span></td><td><button class="action-btn '+(i.enabled?'danger':'')+'" onclick="'+(i.enabled?'confirmDisableBridgeProfile':'enableBridge')+'(\''+i.id+'\')">'+(i.enabled?'禁用':'启用')+'</button></td></tr>';});document.getElementById('bridge-profiles-table').innerHTML=h;}).catch(function(e){document.getElementById('bridge-profiles-table').innerHTML='<tr><td colspan="9" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function createBridgeProfile(){var tenant=document.getElementById('new-bridge-tenant').value;var name=document.getElementById('new-bridge-name').value;var source=document.getElementById('new-bridge-source').value;var target=document.getElementById('new-bridge-target').value;var buffer=parseFloat(document.getElementById('new-bridge-buffer').value)||5;var slippage=parseInt(document.getElementById('new-bridge-slippage').value)||20;var batching=document.getElementById('new-bridge-batching').value;var execution=document.getElementById('new-bridge-execution').value;if(!tenant||!name||!source||!target){alert('请填写完整信息');return;}authFetch(API_BASE+'/asset-bridge/profiles',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({tenant_id:tenant,name:name,source_asset:source,target_asset:target,buffer_percent:buffer,max_slippage_bps:slippage,batching_policy:batching,execution_policy:execution,withdrawal_policy:'AUTO'})}).then(function(r){return r.json().then(function(d){if(r.ok){loadBridgeProfiles();document.getElementById('new-bridge-name').value='';document.getElementById('new-bridge-source').value='';document.getElementById('new-bridge-target').value='';}else{alert('创建失败: '+errMsg(d));}});}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function confirmDisableBridge(id){if(!confirm('确认禁用该桥接配置？禁用后将停止相关资产转换。'))return;authFetch(API_BASE+'/asset-bridge/profiles/'+id+'/disable',{method:'POST'}).then(function(r){if(r.ok){loadBridgeProfiles();}else{r.json().then(function(d){alert('禁用失败: '+errMsg(d));});}}).catch(function(e){alert('禁用失败: '+errMsg(e));});}
function enableBridge(id){authFetch(API_BASE+'/asset-bridge/profiles/'+id+'/enable',{method:'POST'}).then(function(r){if(r.ok){loadBridgeProfiles();}else{r.json().then(function(d){alert('启用失败: '+errMsg(d));});}}).catch(function(e){alert('启用失败: '+errMsg(e));});}
function loadWalletPolicies(){authFetch(API_BASE+'/wallet-policies').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('wallet-policies-table').innerHTML='<tr><td colspan="9" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var status=statusCN[i.status]||i.status;var destType=destTypeCN[i.destination_type]||i.destination_type;h+='<tr><td>'+i.policy_code+'</td><td>'+i.asset_id+'</td><td>'+i.network+'</td><td>'+sh(i.address)+'</td><td>'+destType+'</td><td>'+i.max_single_withdrawal+'</td><td>'+i.daily_limit+'</td><td><span class="badge badge-'+sb(i.status)+'">'+status+'</span></td><td><button class="action-btn '+(i.status==='ACTIVE'?'danger':'')+'" onclick="'+(i.status==='ACTIVE'?'confirmDisableWalletPolicy':'enableWalletPolicy')+'(\''+i.id+'\')">'+(i.status==='ACTIVE'?'禁用':'启用')+'</button></td></tr>';});document.getElementById('wallet-policies-table').innerHTML=h;}).catch(function(e){document.getElementById('wallet-policies-table').innerHTML='<tr><td colspan="9" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function createWalletPolicy(){var tenant=document.getElementById('new-wallet-tenant').value;var code=document.getElementById('new-wallet-code').value;var asset=document.getElementById('new-wallet-asset').value;var network=document.getElementById('new-wallet-network').value;var address=document.getElementById('new-wallet-address').value;var destType=document.getElementById('new-wallet-dest-type').value;var label=document.getElementById('new-wallet-label').value;var singleLimit=parseFloat(document.getElementById('new-wallet-single-limit').value)||0;var dailyLimit=parseFloat(document.getElementById('new-wallet-daily-limit').value)||0;if(!tenant||!code||!asset||!network||!address||!label){alert('请填写完整信息');return;}authFetch(API_BASE+'/wallet-policies',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({tenant_id:tenant,policy_code:code,asset:asset,network:network,address:address,destination_type:destType,label:label,max_single_withdrawal:singleLimit,daily_limit:dailyLimit})}).then(function(r){return r.json().then(function(d){if(r.ok){loadWalletPolicies();document.getElementById('new-wallet-code').value='';document.getElementById('new-wallet-address').value='';document.getElementById('new-wallet-label').value='';}else{alert('创建失败: '+errMsg(d));}});}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function confirmDisableWalletPolicy(id){if(!confirm('确认禁用该钱包策略？禁用后该地址将无法用于提现。'))return;authFetch(API_BASE+'/wallet-policies/'+id+'/disable',{method:'POST'}).then(function(r){if(r.ok){loadWalletPolicies();}else{r.json().then(function(d){alert('禁用失败: '+errMsg(d));});}}).catch(function(e){alert('禁用失败: '+errMsg(e));});}
function enableWalletPolicy(id){authFetch(API_BASE+'/wallet-policies/'+id+'/enable',{method:'POST'}).then(function(r){if(r.ok){loadWalletPolicies();}else{r.json().then(function(d){alert('启用失败: '+errMsg(d));});}}).catch(function(e){alert('启用失败: '+errMsg(e));});}
function loadRiskEvents(){authFetch(API_BASE+'/risk/events').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('risk-events-table').innerHTML='<tr><td colspan="8" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(e){var sevCN={'HIGH':'高危','MEDIUM':'中危','LOW':'低危'}[e.severity]||e.severity;h+='<tr><td>'+sh(e.id)+'</td><td>'+e.event_type+'</td><td>'+sh(e.entity_id)+'</td><td>'+sh(e.channel_id)+'</td><td><span class="badge badge-'+sb(e.severity)+'">'+sevCN+'</span></td><td>'+e.status+'</td><td>'+ft(e.created_at)+'</td><td><button class="action-btn" onclick="showRiskEventDetail(\''+e.id+'\')">详情</button></td></tr>';});document.getElementById('risk-events-table').innerHTML=h;}).catch(function(e){document.getElementById('risk-events-table').innerHTML='<tr><td colspan="8" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function loadRiskHoldStatus(){authFetch(API_BASE+'/entities').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var h='';items.filter(function(e){return e.status==='RISK_HOLD';}).forEach(function(e){h+='<tr><td>法人实体</td><td>'+sh(e.id)+'</td><td>'+e.name+'</td><td><span class="badge badge-err">风险冻结</span></td><td>'+ft(e.updated_at)+'</td><td><button class="action-btn enable" onclick="enableEntity(\''+e.id+'\')">解除冻结</button></td></tr>';});if(!h){h='<tr><td colspan="6" class="empty">暂无风险冻结状态</td></tr>';}document.getElementById('risk-hold-table').innerHTML=h;}).catch(function(e){document.getElementById('risk-hold-table').innerHTML='<tr><td colspan="6" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function loadPendingTasks(){authFetch(API_BASE+'/outbox?status=PENDING').then(function(r){return r.json();}).then(function(d){var items=d.items||[];var h='';items.forEach(function(t){var statusCN={'PENDING':'待处理','UNKNOWN_EXTERNAL_STATE':'外部状态未知','APPROVAL_REQUIRED':'待审批'}[t.status]||t.status;h+='<tr><td>'+sh(t.id)+'</td><td>'+t.event_type+'</td><td><span class="badge badge-warn">'+statusCN+'</span></td><td>'+sh(t.aggregate_id)+'</td><td>'+ft(t.created_at)+'</td><td><button class="action-btn" onclick="showTaskDetail(\''+t.id+'\')">详情</button></td></tr>';});if(!h){h='<tr><td colspan="6" class="empty">暂无待处理任务</td></tr>';}document.getElementById('pending-tasks-table').innerHTML=h;}).catch(function(e){document.getElementById('pending-tasks-table').innerHTML='<tr><td colspan="6" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
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

function loadChannelRiskConfig(){authFetch(API_BASE+'/v3/channel-risk').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('channel-risk-table').innerHTML='<tr><td colspan="8" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var mode=strategyModeCN[i.strategy_mode]||i.strategy_mode;var status=statusCN[i.status]||i.status;var overrideInfo=i.risk_override_enabled?'<span class="badge badge-warn">已覆盖</span>':'-';if(i.risk_override_reason){overrideInfo+='<br><small>'+i.risk_override_reason+'</small>';};h+='<tr><td>'+i.channel_code+'</td><td>'+i.channel_name+'</td><td><span class="badge badge-'+sb(i.strategy_mode)+'">'+mode+'</span></td><td>'+i.chargeback_threshold_bps+' bps</td><td>'+overrideInfo+'</td><td><span class="badge badge-'+sb(i.status)+'">'+status+'</span></td><td>'+ft(i.created_at)+'</td><td><button class="action-btn" onclick="showChannelRiskDetail(\''+i.id+'\')">配置</button></td></tr>';});document.getElementById('channel-risk-table').innerHTML=h;}).catch(function(e){document.getElementById('channel-risk-table').innerHTML='<tr><td colspan="8" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function showChannelRiskDetail(id){var modal=document.createElement('div');modal.className='modal';modal.innerHTML='<div class="modal-content"><h3>通道风控配置</h3><form id="channel-risk-form"><label>风控模式:</label><select id="channel-strategy-mode"><option value="DEFAULT">默认模式</option><option value="STRICT">严格模式</option><option value="MANUAL_REVIEW">人工审核</option></select><label>拒付阈值(bps):</label><input type="number" id="chargeback-threshold" value="500"><label>风险覆盖:</label><input type="checkbox" id="risk-override-enabled"><div id="override-fields" style="display:none"><label>覆盖原因(必填):</label><input type="text" id="override-reason"><label>操作人(必填):</label><input type="text" id="override-operator" value="admin"><label>过期时间(必填):</label><input type="datetime-local" id="override-expires"></div><button type="button" onclick="submitChannelRiskConfig(\''+id+'\')">保存</button><button type="button" onclick="closeModal()">取消</button></form></div>';document.body.appendChild(modal);document.getElementById('risk-override-enabled').onchange=function(){document.getElementById('override-fields').style.display=this.checked?'block':'none';};}
function submitChannelRiskConfig(id){var mode=document.getElementById('channel-strategy-mode').value;var threshold=parseInt(document.getElementById('chargeback-threshold').value)||500;var overrideEnabled=document.getElementById('risk-override-enabled').checked;var reason=null,operator=null,expires=null;if(overrideEnabled){reason=document.getElementById('override-reason').value;operator=document.getElementById('override-operator').value;expires=document.getElementById('override-expires').value;if(!reason||!operator||!expires){alert('覆盖模式下原因、操作人和过期时间必填');return;}}authFetch(API_BASE+'/v3/channel-risk/'+id,{method:'PATCH',headers:{'Content-Type':'application/json'},body:JSON.stringify({strategy_mode:mode,chargeback_threshold_bps:threshold,risk_override_enabled:overrideEnabled,risk_override_reason:reason,risk_override_operator:operator,risk_override_expires_at:expires})}).then(function(r){return r.json().then(function(d){if(r.ok){closeModal();loadChannelRiskConfig();}else{alert('保存失败: '+errMsg(d));}});}).catch(function(e){alert('保存失败: '+errMsg(e));});}
function closeModal(){var modal=document.querySelector('.modal');if(modal)modal.remove();}

function createChannelRiskConfig(){var channelID=document.getElementById('new-channel-id').value;var riskMode=document.getElementById('new-risk-mode').value;if(!channelID){alert('请选择通道');return;}authFetch(API_BASE+'/v3/channel-risk/'+channelID,{method:'PATCH',headers:{'Content-Type':'application/json'},body:JSON.stringify({strategy_mode:riskMode})}).then(function(r){if(r.ok){document.getElementById('new-channel-id').value='';document.getElementById('new-risk-mode').value='DEFAULT';loadChannelRiskConfig();}else{r.json().then(function(d){alert('创建失败: '+errMsg(d));});}}).catch(function(e){alert('创建失败: '+errMsg(e));});}

function loadMerchantRiskProfiles(){authFetch(API_BASE+'/v3/merchant-risk-profiles').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('merchant-risk-table').innerHTML='<tr><td colspan="7" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var status=statusCN[i.status]||i.status;h+='<tr><td>'+sh(i.merchant_id)+'</td><td>'+i.rolling_reserve_rate_bps+' bps</td><td>'+i.dispute_reserve_rate_bps+' bps</td><td>'+i.reserve_release_days+' 天</td><td><span class="badge badge-'+sb(i.status)+'">'+status+'</span></td><td>'+ft(i.created_at)+'</td><td><button class="action-btn" onclick="showMerchantRiskDetail(\''+i.id+'\')">详情</button></td></tr>';});document.getElementById('merchant-risk-table').innerHTML=h;}).catch(function(e){document.getElementById('merchant-risk-table').innerHTML='<tr><td colspan="7" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function showMerchantRiskDetail(id){alert('商户风险配置详情: '+id);}

function createMerchantRiskProfile(){var merchantID=document.getElementById('new-reserve-merchant-id').value;var rollingRate=parseInt(document.getElementById('new-rolling-rate').value)||1000;var disputeRate=parseInt(document.getElementById('new-dispute-rate').value)||500;var releaseDays=parseInt(document.getElementById('new-release-days').value)||30;if(!merchantID){alert('请输入商户ID');return;}authFetch(API_BASE+'/v3/merchant-risk-profiles',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({merchant_id:merchantID,rolling_reserve_rate_bps:rollingRate,dispute_reserve_rate_bps:disputeRate,reserve_release_days:releaseDays})}).then(function(r){if(r.ok){document.getElementById('new-reserve-merchant-id').value='';document.getElementById('new-rolling-rate').value='1000';document.getElementById('new-dispute-rate').value='500';document.getElementById('new-release-days').value='30';loadMerchantRiskProfiles();}else{r.json().then(function(d){alert('创建失败: '+errMsg(d));});}}).catch(function(e){alert('创建失败: '+errMsg(e));});}

function loadRollingReserves(){authFetch(API_BASE+'/v3/rolling-reserves').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('rolling-reserve-table').innerHTML='<tr><td colspan="8" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var type=reserveTypeCN[i.reserve_type]||i.reserve_type;var status=reserveStatusCN[i.status]||i.status;h+='<tr><td>'+sh(i.merchant_id)+'</td><td>'+sh(i.transaction_id)+'</td><td>'+i.amount_minor+'</td><td>'+type+'</td><td>'+ft(i.release_date)+'</td><td><span class="badge badge-'+sb(i.status)+'">'+status+'</span></td><td>'+ft(i.created_at)+'</td><td><button class="action-btn" onclick="showReserveDetail(\''+i.id+'\')">详情</button></td></tr>';});document.getElementById('rolling-reserve-table').innerHTML=h;}).catch(function(e){document.getElementById('rolling-reserve-table').innerHTML='<tr><td colspan="8" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function showReserveDetail(id){alert('准备金记录详情: '+id);}

function loadFeeAllocations(){authFetch(API_BASE+'/v3/fee-allocations').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('fee-allocation-table').innerHTML='<tr><td colspan="8" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var type=feeTypeCN[i.fee_type]||i.fee_type;var status=statusCN[i.status]||i.status;h+='<tr><td>'+sh(i.transaction_id)+'</td><td>'+type+'</td><td>'+i.amount_minor+'</td><td>'+i.currency+'</td><td>'+i.allocation_source+'</td><td><span class="badge badge-'+sb(i.status)+'">'+status+'</span></td><td>'+ft(i.created_at)+'</td><td><button class="action-btn" onclick="showFeeDetail(\''+i.id+'\')">详情</button></td></tr>';});document.getElementById('fee-allocation-table').innerHTML=h;}).catch(function(e){document.getElementById('fee-allocation-table').innerHTML='<tr><td colspan="8" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function showFeeDetail(id){alert('费用摊销详情: '+id);}

function loadIsolationGroups(){authFetch(API_BASE+'/v3/isolation-groups').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('isolation-group-table').innerHTML='<tr><td colspan="7" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var status=isolationStatusCN[i.status]||i.status;var frozenInfo='-';if(i.frozen_reason){frozenInfo='<span class="badge badge-err">冻结</span><br><small>'+i.frozen_reason+' ('+i.frozen_by+')';};h+='<tr><td>'+i.name+'</td><td><span class="badge badge-'+sb(i.status)+'">'+status+'</span></td><td>'+frozenInfo+'</td><td>'+ft(i.created_at)+'</td><td>'+ft(i.updated_at)+'</td><td><button class="action-btn" onclick="showIsolationGroupDetail(\''+i.id+'\')">详情</button></td><td>'+((i.status==='ACTIVE')?'<button class="action-btn danger" onclick="freezeIsolationGroup(\''+i.id+'\')">冻结</button>':'<button class="action-btn enable" onclick="unfreezeIsolationGroup(\''+i.id+'\')">解冻</button>')+'</td></tr>';});document.getElementById('isolation-group-table').innerHTML=h;}).catch(function(e){document.getElementById('isolation-group-table').innerHTML='<tr><td colspan="7" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function showIsolationGroupDetail(id){alert('管道隔离组详情: '+id);}

function createIsolationGroup(){var groupName=document.getElementById('new-isolation-group-name').value;if(!groupName.trim()){alert('请输入隔离组名称');return;}authFetch(API_BASE+'/v3/isolation-groups',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:groupName})}).then(function(r){if(r.ok){document.getElementById('new-isolation-group-name').value='';loadIsolationGroups();}else{r.json().then(function(d){alert('创建失败: '+errMsg(d));});}}).catch(function(e){alert('创建失败: '+errMsg(e));});}

function freezeIsolationGroup(id){var reason=prompt('请输入冻结原因:');if(!reason)return;var operator=prompt('请输入操作人:','admin');if(!operator)return;authFetch(API_BASE+'/v3/isolation-groups/'+id+'/freeze',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({reason:reason,operator:operator})}).then(function(r){if(r.ok){loadIsolationGroups();}else{r.json().then(function(d){alert('冻结失败: '+errMsg(d));});}}).catch(function(e){alert('冻结失败: '+errMsg(e));});}
function unfreezeIsolationGroup(id){var reason=prompt('请输入解冻原因:');if(!reason)return;var operator=prompt('请输入操作人:','admin');if(!operator)return;authFetch(API_BASE+'/v3/isolation-groups/'+id+'/unfreeze',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({reason:reason,operator:operator})}).then(function(r){if(r.ok){loadIsolationGroups();}else{r.json().then(function(d){alert('解冻失败: '+errMsg(d));});}}).catch(function(e){alert('解冻失败: '+errMsg(e));});}

function loadRFIDocuments(){authFetch(API_BASE+'/v3/rfi-documents').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('rfi-documents-table').innerHTML='<tr><td colspan="8" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var status=rfiStatusCN[i.status]||i.status;var reviewInfo=i.review_required_reason?'<br><small class="warn">'+i.review_required_reason+'</small>':'';var actions='<button class="action-btn" onclick="showRFIDetail(\''+i.id+'\')">详情</button>';if(i.status==='PENDING'||i.status==='REVIEW_REQUIRED'){actions+=' <button class="action-btn enable" onclick="confirmApproveRFIDoc(\''+i.id+'\')">通过</button> <button class="action-btn danger" onclick="confirmRejectRFIDoc(\''+i.id+'\')">拒绝</button>';};h+='<tr><td>'+sh(i.id)+'</td><td>'+sh(i.order_id)+'</td><td>'+sh(i.merchant_id)+'</td><td>'+i.document_type+'</td><td><span class="badge badge-'+sb(i.status)+'">'+status+'</span>'+reviewInfo+'</td><td>'+i.source_data_hash+'</td><td>'+ft(i.created_at)+'</td><td>'+actions+'</td></tr>';});document.getElementById('rfi-documents-table').innerHTML=h;}).catch(function(e){document.getElementById('rfi-documents-table').innerHTML='<tr><td colspan="8" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function showRFIDetail(id){authFetch(API_BASE+'/v3/rfi-documents/'+id).then(function(r){return r.json();}).then(function(d){var info='RFI资料包详情\n\n';info+='ID: '+d.id+'\n';info+='订单ID: '+d.order_id+'\n';info+='商户ID: '+d.merchant_id+'\n';info+='类型: '+d.document_type+'\n';info+='状态: '+(rfiStatusCN[d.status]||d.status)+'\n';info+='源数据哈希: '+d.source_data_hash+'\n';info+='创建时间: '+d.created_at+'\n';if(d.approved_at){info+='审批时间: '+d.approved_at;};if(d.rejected_at){info+='\n拒绝时间: '+d.rejected_at;};if(d.rejected_reason){info+='\n拒绝原因: '+d.rejected_reason;};alert(info);}).catch(function(e){alert('加载详情失败: '+errMsg(e));});}
function confirmApproveRFIDoc(id){if(!confirm('确认通过该RFI资料包？'))return;var operator=prompt('请输入操作人:','admin');if(!operator)return;authFetch(API_BASE+'/v3/rfi-documents/'+id+'/approve',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({operator:operator})}).then(function(r){if(r.ok){alert('审批通过成功');loadRFIDocuments();}else{r.json().then(function(d){alert('审批失败: '+errMsg(d));});}}).catch(function(e){alert('审批失败: '+errMsg(e));});}
function confirmRejectRFIDoc(id){var reason=prompt('请输入拒绝原因:');if(!reason)return;var operator=prompt('请输入操作人:','admin');if(!operator)return;authFetch(API_BASE+'/v3/rfi-documents/'+id+'/reject',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({reason:reason,operator:operator})}).then(function(r){if(r.ok){alert('已拒绝');loadRFIDocuments();}else{r.json().then(function(d){alert('拒绝失败: '+errMsg(d));});}}).catch(function(e){alert('拒绝失败: '+errMsg(e));});}

function generateRFIDocument(){var orderID=document.getElementById('new-rfi-order').value;var docType=document.getElementById('new-rfi-type').value;if(!orderID){alert('请输入订单ID');return;}if(!docType){alert('请选择文档类型');return;}authFetch(API_BASE+'/v3/rfi-documents/generate',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({order_id:orderID,document_type:docType})}).then(function(r){if(r.ok){document.getElementById('new-rfi-order').value='';document.getElementById('new-rfi-type').value='INVOICE';loadRFIDocuments();}else{r.json().then(function(d){alert('生成失败: '+errMsg(d));});}}).catch(function(e){alert('生成失败: '+errMsg(e));});}

function loadOverrideAuditLogs(){authFetch(API_BASE+'/v3/override-audit-logs').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('override-audit-table').innerHTML='<tr><td colspan="7" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var type=overrideTypeCN[i.override_type]||i.override_type;var expires=i.expires_at?'<br><small>过期: '+ft(i.expires_at)+'</small>':'';h+='<tr><td>'+sh(i.id)+'</td><td>'+i.entity_type+'</td><td>'+sh(i.entity_id)+'</td><td>'+type+'</td><td>'+i.reason+'</td><td>'+i.operator+expires+'</td><td>'+ft(i.created_at)+'</td></tr>';});document.getElementById('override-audit-table').innerHTML=h;}).catch(function(e){document.getElementById('override-audit-table').innerHTML='<tr><td colspan="7" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}

function loadAcquirerChannels(){authFetch(API_BASE+'/api/v3/channels').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('acquirer-channels-table').innerHTML='<tr><td colspan="9" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var status=statusCN[i.status]||i.status;var strategyMode=i.strategy_mode||'DEFAULT';var burnBadge=strategyMode==='BURN_MODE'?'<span class="badge badge-err">BURN_MODE</span>':'';var actions='';if(i.status==='ACTIVE'){actions+='<button class="action-btn danger" onclick="disableAcquirerChannel(\''+i.id+'\')">禁用</button>';}else{actions+='<button class="action-btn enable" onclick="enableAcquirerChannel(\''+i.id+'\')">启用</button>';}actions+=' <button class="action-btn" onclick="reloadAcquirerChannel(\''+i.id+'\')">重载</button>';h+='<tr><td>'+sh(i.id)+'</td><td>'+sh(i.entity_id)+'</td><td>'+i.provider_tier+'</td><td>'+i.provider_code+'</td><td>'+i.channel_code+'</td><td>'+strategyMode+' '+burnBadge+'</td><td><span class="badge badge-'+sb(i.status)+'">'+status+'</span></td><td>'+ft(i.created_at)+'</td><td>'+actions+'</td></tr>';});document.getElementById('acquirer-channels-table').innerHTML=h;}).catch(function(e){document.getElementById('acquirer-channels-table').innerHTML='<tr><td colspan="9" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function createAcquirerChannel(){var entityID=document.getElementById('new-acq-entity-id').value;var providerTier=document.getElementById('new-acq-provider-tier').value;var providerCode=document.getElementById('new-acq-provider-code').value;var channelCode=document.getElementById('new-acq-channel-code').value;var vaultKey=document.getElementById('new-acq-vault-key').value;var strategyMode=document.getElementById('new-acq-strategy-mode').value;if(!entityID||!providerCode||!channelCode||!vaultKey){alert('请填写完整信息');return;}authFetch(API_BASE+'/api/v3/channels',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({entity_id:entityID,provider_tier:providerTier,provider_code:providerCode,channel_code:channelCode,credential_vault_key:vaultKey,strategy_mode:strategyMode,provider_config:{},routing_config:{}})}).then(function(r){if(r.ok){document.getElementById('new-acq-entity-id').value='';document.getElementById('new-acq-provider-code').value='';document.getElementById('new-acq-channel-code').value='';document.getElementById('new-acq-vault-key').value='';document.getElementById('new-acq-strategy-mode').value='DEFAULT';loadAcquirerChannels();}else{r.json().then(function(d){alert('创建失败: '+errMsg(d));});}}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function enableAcquirerChannel(id){authFetch(API_BASE+'/api/v3/channels/'+id+'/enable',{method:'POST'}).then(function(r){if(r.ok){loadAcquirerChannels();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}
function disableAcquirerChannel(id){if(!confirm('确认禁用该收单通道？禁用后该通道将无法处理交易。'))return;authFetch(API_BASE+'/api/v3/channels/'+id+'/disable',{method:'POST'}).then(function(r){if(r.ok){loadAcquirerChannels();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}
function reloadAcquirerChannel(id){authFetch(API_BASE+'/api/v3/channels/'+id+'/reload',{method:'POST'}).then(function(r){if(r.ok){alert('通道重载请求已发送');loadAcquirerChannels();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}

function loadIssuingPools(){authFetch(API_BASE+'/api/v3/issuing/pools').then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById('issuing-pools-table').innerHTML='<tr><td colspan="9" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){var status=statusCN[i.status]||i.status;var actions='';if(i.status==='ACTIVE'){actions+='<button class="action-btn danger" onclick="disableIssuingPool(\''+i.id+'\')">禁用</button>';}else{actions+='<button class="action-btn enable" onclick="enableIssuingPool(\''+i.id+'\')">启用</button>';}actions+=' <button class="action-btn" onclick="reloadIssuingPool(\''+i.id+'\')">重载</button>';h+='<tr><td>'+sh(i.id)+'</td><td>'+sh(i.entity_id)+'</td><td>'+i.issuing_tier+'</td><td>'+i.funding_mode+'</td><td>'+i.provider_code+'</td><td>'+i.pool_code+'</td><td><span class="badge badge-'+sb(i.status)+'">'+status+'</span></td><td>'+ft(i.created_at)+'</td><td>'+actions+'</td></tr>';});document.getElementById('issuing-pools-table').innerHTML=h;}).catch(function(e){document.getElementById('issuing-pools-table').innerHTML='<tr><td colspan="9" class="empty">加载失败: '+errMsg(e)+'</td></tr>';});}
function createIssuingPool(){var entityID=document.getElementById('new-iss-entity-id').value;var issuingTier=document.getElementById('new-iss-issuing-tier').value;var fundingMode=document.getElementById('new-iss-funding-mode').value;var providerCode=document.getElementById('new-iss-provider-code').value;var poolCode=document.getElementById('new-iss-pool-code').value;var vaultKey=document.getElementById('new-iss-vault-key').value;if(!providerCode||!poolCode||!vaultKey){alert('请填写完整信息');return;}authFetch(API_BASE+'/api/v3/issuing/pools',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({entity_id:entityID,issuing_tier:issuingTier,funding_mode:fundingMode,provider_code:providerCode,pool_code:poolCode,credential_vault_key:vaultKey,provider_config:{},velocity_config:{}})}).then(function(r){if(r.ok){document.getElementById('new-iss-entity-id').value='';document.getElementById('new-iss-provider-code').value='';document.getElementById('new-iss-pool-code').value='';document.getElementById('new-iss-vault-key').value='';document.getElementById('new-iss-funding-mode').value='FIAT_DIRECT';loadIssuingPools();}else{r.json().then(function(d){alert('创建失败: '+errMsg(d));});}}).catch(function(e){alert('创建失败: '+errMsg(e));});}
function enableIssuingPool(id){authFetch(API_BASE+'/api/v3/issuing/pools/'+id+'/enable',{method:'POST'}).then(function(r){if(r.ok){loadIssuingPools();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}
function disableIssuingPool(id){if(!confirm('确认禁用该发卡卡池？禁用后该卡池将无法开卡。'))return;authFetch(API_BASE+'/api/v3/issuing/pools/'+id+'/disable',{method:'POST'}).then(function(r){if(r.ok){loadIssuingPools();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}
function reloadIssuingPool(id){authFetch(API_BASE+'/api/v3/issuing/pools/'+id+'/reload',{method:'POST'}).then(function(r){if(r.ok){alert('卡池重载请求已发送');loadIssuingPools();}else{r.json().then(function(d){alert('操作失败: '+errMsg(d));});}}).catch(function(e){alert('操作失败: '+errMsg(e));});}

// V3 Tab 初始化函数
function initV3Tabs(){if(document.getElementById('tab-channel-risk').classList.contains('active'))loadChannelRiskConfig();if(document.getElementById('tab-merchant-reserve').classList.contains('active')){loadMerchantRiskProfiles();loadRollingReserves();}if(document.getElementById('tab-fee-allocation').classList.contains('active'))loadFeeAllocations();if(document.getElementById('tab-pipeline-isolation').classList.contains('active'))loadIsolationGroups();if(document.getElementById('tab-compliance-rfi').classList.contains('active'))loadRFIDocuments();}
loadStats();
</script>
</body>
</html>`
}
