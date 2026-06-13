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
		dbStatus, dbClass := "Disconnected", "error"
		if pool != nil {
			if err := pool.Ping(r.Context()); err == nil {
				dbStatus, dbClass = "Connected", "ok"
			}
		}
		redisStatus, redisClass := "Disconnected", "error"
		if rdb != nil {
			if err := rdb.Ping(r.Context()).Err(); err == nil {
				redisStatus, redisClass = "Connected", "ok"
			}
		}
		uptime := time.Since(startTime).Round(time.Second)
		// G9: 不回显 token，避免 self-XSS 风险
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, dashboardHTML(env, dbStatus, dbClass, redisStatus, redisClass, uptime.String(), runtime.Version(), runtime.NumCPU(), runtime.NumGoroutine(), mem.Alloc/1024/1024, mem.Sys/1024/1024))
	}
}

func dashboardHTML(env, dbStatus, dbClass, redisStatus, redisClass, uptime, goVer string, cpu, goroutines int, memAlloc, memSys uint64) string {
	dbCN := "已连接"
	if dbClass == "error" {
		dbCN = "未连接"
	}
	redisCN := "已连接"
	if redisClass == "error" {
		redisCN = "未连接"
	}
	return `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0">
<title>NexusGlobal MAMR - 管理后台</title>
<style>
:root{--bg:#0f172a;--card:#1e293b;--border:#334155;--text:#e2e8f0;--muted:#64748b;--accent:#3b82f6;--ok:#22c55e;--err:#ef4444;--warn:#f59e0b}
*{margin:0;padding:0;box-sizing:border-box}body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','Microsoft YaHei',sans-serif;background:var(--bg);color:var(--text);min-height:100vh}
.nav{background:var(--card);border-bottom:1px solid var(--border);padding:0 24px;display:flex;align-items:center;height:56px;gap:8px;overflow-x:auto}
.nav .logo{font-weight:700;font-size:16px;color:#f8fafc;margin-right:24px;white-space:nowrap}
.nav button{background:none;border:none;color:var(--muted);padding:8px 16px;cursor:pointer;font-size:13px;border-radius:6px;white-space:nowrap}
.nav button:hover{background:#334155;color:var(--text)}.nav button.active{background:var(--accent);color:#fff}
.main{max-width:1400px;margin:0 auto;padding:24px}.tab{display:none}.tab.active{display:block}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:16px;margin-bottom:24px}
.stat{background:var(--card);border:1px solid var(--border);border-radius:10px;padding:20px}
.stat .label{font-size:11px;letter-spacing:.5px;color:var(--muted);margin-bottom:8px}
.stat .value{font-size:28px;font-weight:700;color:#f8fafc}.stat .sub{font-size:12px;color:var(--muted);margin-top:4px}
.badge{display:inline-block;padding:2px 8px;border-radius:10px;font-size:11px;font-weight:600}
.badge-ok{background:#052e16;color:var(--ok)}.badge-err{background:#2a0a0a;color:var(--err)}
.badge-warn{background:#422006;color:var(--warn)}.badge-info{background:#172554;color:var(--accent)}
table{width:100%;border-collapse:collapse;background:var(--card);border-radius:10px;overflow:hidden;border:1px solid var(--border)}
th{text-align:left;padding:10px 14px;background:#1a2332;color:var(--muted);font-size:11px;letter-spacing:.5px;border-bottom:1px solid var(--border)}
td{padding:10px 14px;border-bottom:1px solid #1e293b;font-size:13px;font-family:'SF Mono','Fira Code',monospace}
tr:hover td{background:#1a2332}.section{margin-bottom:24px}
.section h2{font-size:18px;font-weight:600;color:#f8fafc;margin-bottom:16px;padding-bottom:8px;border-bottom:1px solid var(--border)}
.card{background:var(--card);border:1px solid var(--border);border-radius:10px;padding:20px;margin-bottom:16px}
pre{color:var(--muted);font-size:12px;line-height:1.6;overflow-x:auto;white-space:pre-wrap}
.method{display:inline-block;padding:2px 6px;border-radius:4px;font-size:10px;font-weight:700;font-family:monospace;min-width:40px;text-align:center}
.m-get{background:#052e16;color:var(--ok)}.m-post{background:#172554;color:var(--accent)}.m-patch{background:#422006;color:var(--warn)}
.status-dot{display:inline-block;width:8px;height:8px;border-radius:50%;margin-right:6px}
.dot-ok{background:var(--ok)}.dot-err{background:var(--err)}
.refresh-btn{background:var(--accent);color:#fff;border:none;padding:6px 14px;border-radius:6px;cursor:pointer;font-size:12px}
.refresh-btn:hover{opacity:.8}.loading{color:var(--muted);font-style:italic;padding:20px}
.empty{color:var(--muted);text-align:center;padding:40px}
</style></head><body>
<div class="nav"><span class="logo">NexusGlobal MAMR</span>
<button class="active" onclick="showTab('overview',this)">总览</button>
<button onclick="showTab('mae',this)">收单路由</button>
<button onclick="showTab('payout',this)">代付管理</button>
<button onclick="showTab('treasury',this)">资金归集</button>
<button onclick="showTab('webhook',this)">回调事件</button>
<button onclick="showTab('ledger',this)">账务明细</button>
<button onclick="showTab('outbox',this)">异步任务</button>
<button onclick="showTab('merchants',this)">商户余额</button>
<button onclick="showTab('endpoints',this)">接口清单</button>
<button onclick="showTab('system',this)">系统信息</button>
</div>
<div class="main">
<div id="tab-overview" class="tab active"><div class="grid">
<div class="stat"><div class="label">系统状态</div><div class="value"><span class="status-dot dot-ok"></span>运行中</div><div class="sub">运行时间: ` + uptime + `</div></div>
<div class="stat"><div class="label">PostgreSQL 数据库</div><div class="value"><span class="status-dot dot-` + dbClass + `"></span>` + dbCN + `</div><div class="sub">主数据存储</div></div>
<div class="stat"><div class="label">Redis 缓存</div><div class="value"><span class="status-dot dot-` + redisClass + `"></span>` + redisCN + `</div><div class="sub">消息队列 + 缓存</div></div>
<div class="stat"><div class="label">运行环境</div><div class="value">` + env + `</div><div class="sub">Go ` + goVer + `</div></div>
<div class="stat"><div class="label">内存使用</div><div class="value">` + fmt.Sprint(memAlloc) + ` MB</div><div class="sub">已分配 / 系统总计</div></div>
<div class="stat"><div class="label">协程数</div><div class="value">` + fmt.Sprint(goroutines) + `</div><div class="sub">活跃 goroutine</div></div>
</div><div class="section"><h2>业务统计 <button class="refresh-btn" onclick="loadStats()">刷新</button></h2><div id="stats-content" class="loading">加载中...</div></div></div>
<div id="tab-mae" class="tab"><div class="section"><h2>收单路由配置 <button class="refresh-btn" onclick="loadList('mae-list','mae-table')">刷新</button></h2>
<table><thead><tr><th>MAE 编号</th><th>名称</th><th>类型</th><th>状态</th><th>版本</th><th>日限额</th><th>路由权重</th><th>创建时间</th></tr></thead><tbody id="mae-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div></div>
<div id="tab-payout" class="tab"><div class="section"><h2>代付请求 <button class="refresh-btn" onclick="loadList('payouts','payout-table')">刷新</button></h2>
<table><thead><tr><th>请求ID</th><th>商户ID</th><th>金额</th><th>法币</th><th>目标资产</th><th>状态</th><th>版本</th><th>创建时间</th></tr></thead><tbody id="payout-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div></div>
<div id="tab-treasury" class="tab">
<div class="section"><h2>银行日历 <button class="refresh-btn" onclick="loadList('calendars','cal-table')">刷新</button></h2>
<table><thead><tr><th>ID</th><th>日历名称</th><th>司法辖区</th><th>时区</th><th>状态</th><th>创建时间</th></tr></thead><tbody id="cal-table"><tr><td colspan="6" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>归集规则 <button class="refresh-btn" onclick="loadList('sweep-rules','rule-table')">刷新</button></h2>
<table><thead><tr><th>ID</th><th>币种</th><th>通道</th><th>受益人类型</th><th>触发阈值</th><th>日最大次数</th><th>模式</th><th>状态</th></tr></thead><tbody id="rule-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div>
<div class="section"><h2>归集转账 <button class="refresh-btn" onclick="loadList('sweep-transfers','transfer-table')">刷新</button></h2>
<table><thead><tr><th>ID</th><th>规则ID</th><th>金额</th><th>币种</th><th>通道</th><th>状态</th><th>银行参考号</th><th>创建时间</th></tr></thead><tbody id="transfer-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div></div>
<div id="tab-webhook" class="tab"><div class="section"><h2>回调事件 <button class="refresh-btn" onclick="loadList('webhook-events','wh-table')">刷新</button></h2>
<table><thead><tr><th>ID</th><th>提供方类型</th><th>提供方ID</th><th>事件类型</th><th>状态</th><th>创建时间</th></tr></thead><tbody id="wh-table"><tr><td colspan="6" class="loading">加载中...</td></tr></tbody></table></div></div>
<div id="tab-ledger" class="tab"><div class="section"><h2>账务交易 <button class="refresh-btn" onclick="loadList('ledger/transactions','txn-table')">刷新</button></h2>
<table><thead><tr><th>ID</th><th>幂等键</th><th>交易类型</th><th>聚合类型</th><th>描述</th><th>创建时间</th></tr></thead><tbody id="txn-table"><tr><td colspan="6" class="loading">加载中...</td></tr></tbody></table></div></div>
<div id="tab-outbox" class="tab"><div class="section"><h2>异步任务队列 <button class="refresh-btn" onclick="loadList('outbox','outbox-table')">刷新</button></h2>
<table><thead><tr><th>ID</th><th>聚合类型</th><th>事件类型</th><th>状态</th><th>重试次数</th><th>创建时间</th></tr></thead><tbody id="outbox-table"><tr><td colspan="6" class="loading">加载中...</td></tr></tbody></table></div></div>
<div id="tab-merchants" class="tab"><div class="section"><h2>商户余额总账 <button class="refresh-btn" onclick="loadList('merchants','merchant-table')">刷新</button></h2>
<table><thead><tr><th>商户ID</th><th>币种</th><th>已结算</th><th>已冻结</th><th>准备金</th><th>争议冻结</th><th>版本</th><th>更新时间</th></tr></thead><tbody id="merchant-table"><tr><td colspan="8" class="loading">加载中...</td></tr></tbody></table></div></div>
<div id="tab-endpoints" class="tab"><div class="section"><h2>接口清单</h2><div class="card"><pre>
<span style="color:#94a3b8">── 系统运维 ──</span>
<span class="method m-get">GET</span>  /healthz                              健康检查（存活探针）
<span class="method m-get">GET</span>  /readyz                               就绪检查（PG+Redis）
<span class="method m-get">GET</span>  /admin/dashboard                      管理后台（HTML）
<span class="method m-get">GET</span>  /admin/stats                          系统统计数据

<span style="color:#94a3b8">── 收单路由管理 ──</span>
<span class="method m-get">GET</span>  /admin/mae-list                       收单路由列表
<span class="method m-get">GET</span>  /admin/mae-detail/{mae_id}            收单路由详情
<span class="method m-post">POST</span> /admin/mae                            创建收单路由
<span class="method m-patch">PATCH</span>/admin/mae/{mae_id}                  更新收单路由
<span class="method m-post">POST</span> /admin/mae/{mae_id}/rotate-secret    轮换凭证
<span class="method m-post">POST</span> /admin/mae/{mae_id}/status           变更状态
<span class="method m-get">GET</span>  /admin/mae/{mae_id}/metrics           查询指标

<span style="color:#94a3b8">── 代付管理 ──</span>
<span class="method m-get">GET</span>  /admin/payouts                        代付请求列表
<span class="method m-get">GET</span>  /admin/payout/{id}                    代付请求详情
<span class="method m-post">POST</span> /merchant/payout-address              添加出金地址
<span class="method m-post">POST</span> /merchant/payout-request              创建代付请求
<span class="method m-post">POST</span> /internal/payout/{id}/freeze         冻结余额（阶段一）
<span class="method m-post">POST</span> /internal/payout/{id}/fx-lock        锁定汇率（阶段二）
<span class="method m-post">POST</span> /internal/payout/{id}/submit         提交代付（阶段三）
<span class="method m-post">POST</span> /internal/payout/{id}/reconcile      对账确认

<span style="color:#94a3b8">── 资金归集 ──</span>
<span class="method m-get">GET</span>  /admin/calendars                      银行日历列表
<span class="method m-get">GET</span>  /admin/sweep-rules                    归集规则列表
<span class="method m-get">GET</span>  /admin/sweep-transfers                归集转账列表
<span class="method m-post">POST</span> /admin/banking-calendar               创建银行日历
<span class="method m-post">POST</span> /admin/sweep-rule                     创建归集规则
<span class="method m-post">POST</span> /internal/sweep/evaluate              触发归集评估
<span class="method m-post">POST</span> /internal/sweep/{id}/submit           提交归集转账
<span class="method m-post">POST</span> /internal/sweep/{id}/reconcile        归集对账

<span style="color:#94a3b8">── 回调事件 ──</span>
<span class="method m-get">GET</span>  /admin/webhook-events                 回调事件列表
<span class="method m-get">GET</span>  /admin/webhook-event/{id}             回调事件详情
<span class="method m-post">POST</span> /webhook/mae/{provider_id}            收单回调接收
<span class="method m-post">POST</span> /webhook/bank/{provider_id}           银行回调接收
<span class="method m-post">POST</span> /webhook/lp/{provider_id}             渠道回调接收

<span style="color:#94a3b8">── 账务明细 ──</span>
<span class="method m-get">GET</span>  /admin/merchant/{id}/balance           商户余额查询
<span class="method m-get">GET</span>  /admin/merchants                      商户列表
<span class="method m-get">GET</span>  /admin/ledger/transactions            账务交易列表
<span class="method m-get">GET</span>  /admin/ledger/entries                 账务分录列表

<span style="color:#94a3b8">── 异步任务 ──</span>
<span class="method m-get">GET</span>  /admin/outbox                         任务队列列表
</pre></div></div></div>
<div id="tab-system" class="tab"><div class="section"><h2>系统信息</h2><div class="card"><table>
<tr><td style="width:200px;color:var(--muted)">Go 版本</td><td>` + goVer + `</td></tr>
<tr><td style="color:var(--muted)">CPU 核心数</td><td>` + fmt.Sprint(cpu) + `</td></tr>
<tr><td style="color:var(--muted)">活跃协程数</td><td>` + fmt.Sprint(goroutines) + `</td></tr>
<tr><td style="color:var(--muted)">内存已分配</td><td>` + fmt.Sprint(memAlloc) + ` MB</td></tr>
<tr><td style="color:var(--muted)">内存系统总计</td><td>` + fmt.Sprint(memSys) + ` MB</td></tr>
<tr><td style="color:var(--muted)">运行时间</td><td>` + uptime + `</td></tr>
<tr><td style="color:var(--muted)">运行环境</td><td>` + env + `</td></tr>
<tr><td style="color:var(--muted)">PostgreSQL</td><td><span class="status-dot dot-` + dbClass + `"></span>` + dbCN + `</td></tr>
<tr><td style="color:var(--muted)">Redis</td><td><span class="status-dot dot-` + redisClass + `"></span>` + redisCN + `</td></tr>
</table></div></div></div>
</div>
<script>
var API_BASE='/admin';var AUTH_TOKEN='';
function showTab(name,btn){document.querySelectorAll('.tab').forEach(function(t){t.classList.remove('active');});document.querySelectorAll('.nav button').forEach(function(b){b.classList.remove('active');});document.getElementById('tab-'+name).classList.add('active');btn.classList.add('active');if(name==='overview')loadStats();}
function loadStats(){fetch(API_BASE+'/stats',{headers:{'Authorization':AUTH_TOKEN}}).then(function(r){return r.json();}).then(function(d){var h='<div class="grid">';h+='<div class="stat"><div class="label">收单路由</div><div class="value">'+(d.mae_count||0)+'</div><div class="sub">个配置</div></div>';h+='<div class="stat"><div class="label">代付请求</div><div class="value">'+(d.payout_count||0)+'</div><div class="sub">笔请求</div></div>';h+='<div class="stat"><div class="label">待处理任务</div><div class="value">'+(d.outbox_pending||0)+'</div><div class="sub">条待处理</div></div>';h+='<div class="stat"><div class="label">回调事件</div><div class="value">'+(d.webhook_count||0)+'</div><div class="sub">条事件</div></div>';h+='<div class="stat"><div class="label">归集转账</div><div class="value">'+(d.sweep_count||0)+'</div><div class="sub">笔转账</div></div>';h+='<div class="stat"><div class="label">商户数量</div><div class="value">'+(d.merchant_count||0)+'</div><div class="sub">个商户</div></div>';h+='</div>';if(d.payout_by_status&&d.payout_by_status.length){h+='<h3 style="margin:16px 0 8px;color:var(--muted);font-size:13px">代付状态分布</h3><div class="grid">';d.payout_by_status.forEach(function(s){h+='<div class="stat"><div class="label">'+s.status+'</div><div class="value">'+s.count+'</div><div class="sub">笔</div></div>';});h+='</div>';}document.getElementById('stats-content').innerHTML=h;}).catch(function(){document.getElementById('stats-content').innerHTML='<div class="empty">加载失败</div>';});}
function loadList(ep,tid){fetch(API_BASE+'/'+ep,{headers:{'Authorization':AUTH_TOKEN}}).then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById(tid).innerHTML='<tr><td colspan="10" class="empty">暂无数据</td></tr>';return;}var h='';items.forEach(function(i){h+='<tr>';if(ep==='mae-list'){h+='<td>'+i.mae_id+'</td><td>'+i.display_name+'</td><td>'+i.mae_type+'</td><td><span class="badge badge-'+sb(i.status)+'">'+i.status+'</span></td><td>'+i.version+'</td><td>'+i.daily_vmax_minor+'</td><td>'+i.routing_weight+'</td><td>'+ft(i.created_at)+'</td>';}else if(ep==='payouts'){h+='<td>'+sh(i.id)+'</td><td>'+sh(i.merchant_id)+'</td><td>'+i.amount+'</td><td>'+i.fiat_currency+'</td><td>'+i.target_asset+'</td><td><span class="badge badge-'+sb(i.status)+'">'+i.status+'</span></td><td>'+i.version+'</td><td>'+ft(i.created_at)+'</td>';}else if(ep==='calendars'){h+='<td>'+sh(i.id)+'</td><td>'+i.calendar_name+'</td><td>'+i.jurisdiction+'</td><td>'+i.timezone+'</td><td><span class="badge badge-ok">'+i.status+'</span></td><td>'+ft(i.created_at)+'</td>';}else if(ep==='sweep-rules'){h+='<td>'+sh(i.id)+'</td><td>'+i.currency+'</td><td>'+i.rail+'</td><td>'+i.beneficiary_type+'</td><td>'+i.trigger_threshold_minor+'</td><td>'+i.max_daily_sweep_count+'</td><td>'+i.sweep_mode+'</td><td><span class="badge badge-ok">'+i.status+'</span></td>';}else if(ep==='sweep-transfers'){h+='<td>'+sh(i.id)+'</td><td>'+sh(i.sweep_rule_id)+'</td><td>'+i.amount_minor+'</td><td>'+i.currency+'</td><td>'+i.rail+'</td><td><span class="badge badge-'+sb(i.status)+'">'+i.status+'</span></td><td>'+i.bank_ref+'</td><td>'+ft(i.created_at)+'</td>';}else if(ep==='webhook-events'){h+='<td>'+sh(i.id)+'</td><td>'+i.provider_type+'</td><td>'+i.provider_id+'</td><td>'+i.event_type+'</td><td><span class="badge badge-'+sb(i.status)+'">'+i.status+'</span></td><td>'+ft(i.created_at)+'</td>';}else if(ep==='ledger/transactions'){h+='<td>'+sh(i.id)+'</td><td>'+i.idempotency_key+'</td><td>'+i.transaction_type+'</td><td>'+i.aggregate_type+'</td><td>'+i.description+'</td><td>'+ft(i.created_at)+'</td>';}else if(ep==='outbox'){h+='<td>'+sh(i.id)+'</td><td>'+i.aggregate_type+'</td><td>'+i.event_type+'</td><td><span class="badge badge-'+sb(i.status)+'">'+i.status+'</span></td><td>'+i.retry_count+'</td><td>'+ft(i.created_at)+'</td>';}else if(ep==='merchants'){h+='<td>'+sh(i.merchant_id)+'</td><td>'+i.currency+'</td><td>'+i.settled_balance+'</td><td>'+i.frozen_payout_balance+'</td><td>'+i.reserve_balance+'</td><td>'+i.dispute_hold_balance+'</td><td>'+i.version+'</td><td>'+ft(i.updated_at)+'</td>';}h+='</tr>';});document.getElementById(tid).innerHTML=h;}).catch(function(){document.getElementById(tid).innerHTML='<tr><td colspan="10" class="empty">加载失败</td></tr>';});}
function sh(id){return id?String(id).substring(0,8)+'...':'-';}
function ft(t){return t?String(t).substring(0,19).replace('T',' '):'-';}
function sb(s){if(!s)return'info';s=s.toLowerCase();if(s==='active'||s==='confirmed'||s==='reconciled'||s==='completed'||s==='submitted')return'ok';if(s==='failed'||s==='rejected'||s==='deadletter')return'err';if(s==='cooldown'||s==='unknownexternalstate'||s==='pending')return'warn';return'info';}
loadStats();
</script></body></html>`
}
