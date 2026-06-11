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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, dashboardHTML(env, dbStatus, dbClass, redisStatus, redisClass, uptime.String(), runtime.Version(), runtime.NumCPU(), runtime.NumGoroutine(), mem.Alloc/1024/1024, mem.Sys/1024/1024))
	}
}

func dashboardHTML(env, dbStatus, dbClass, redisStatus, redisClass, uptime, goVer string, cpu, goroutines int, memAlloc, memSys uint64) string {
	return `<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0">
<title>NexusGlobal MAMR - Admin Console</title>
<style>
:root{--bg:#0f172a;--card:#1e293b;--border:#334155;--text:#e2e8f0;--muted:#64748b;--accent:#3b82f6;--ok:#22c55e;--err:#ef4444;--warn:#f59e0b}
*{margin:0;padding:0;box-sizing:border-box}body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:var(--bg);color:var(--text);min-height:100vh}
.nav{background:var(--card);border-bottom:1px solid var(--border);padding:0 24px;display:flex;align-items:center;height:56px;gap:8px;overflow-x:auto}
.nav .logo{font-weight:700;font-size:16px;color:#f8fafc;margin-right:24px;white-space:nowrap}
.nav button{background:none;border:none;color:var(--muted);padding:8px 16px;cursor:pointer;font-size:13px;border-radius:6px;white-space:nowrap}
.nav button:hover{background:#334155;color:var(--text)}.nav button.active{background:var(--accent);color:#fff}
.main{max-width:1400px;margin:0 auto;padding:24px}.tab{display:none}.tab.active{display:block}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:16px;margin-bottom:24px}
.stat{background:var(--card);border:1px solid var(--border);border-radius:10px;padding:20px}
.stat .label{font-size:11px;text-transform:uppercase;letter-spacing:.5px;color:var(--muted);margin-bottom:8px}
.stat .value{font-size:28px;font-weight:700;color:#f8fafc}.stat .sub{font-size:12px;color:var(--muted);margin-top:4px}
.badge{display:inline-block;padding:2px 8px;border-radius:10px;font-size:11px;font-weight:600}
.badge-ok{background:#052e16;color:var(--ok)}.badge-err{background:#2a0a0a;color:var(--err)}
.badge-warn{background:#422006;color:var(--warn)}.badge-info{background:#172554;color:var(--accent)}
table{width:100%;border-collapse:collapse;background:var(--card);border-radius:10px;overflow:hidden;border:1px solid var(--border)}
th{text-align:left;padding:10px 14px;background:#1a2332;color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.5px;border-bottom:1px solid var(--border)}
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
<button class="active" onclick="showTab('overview',this)">Overview</button>
<button onclick="showTab('mae',this)">MAE</button>
<button onclick="showTab('payout',this)">Payout</button>
<button onclick="showTab('treasury',this)">Treasury</button>
<button onclick="showTab('webhook',this)">Webhook</button>
<button onclick="showTab('ledger',this)">Ledger</button>
<button onclick="showTab('outbox',this)">Outbox</button>
<button onclick="showTab('merchants',this)">Merchants</button>
<button onclick="showTab('endpoints',this)">API</button>
<button onclick="showTab('system',this)">System</button>
</div>
<div class="main">
<div id="tab-overview" class="tab active"><div class="grid">
<div class="stat"><div class="label">System</div><div class="value"><span class="status-dot dot-ok"></span>Online</div><div class="sub">Uptime: ` + uptime + `</div></div>
<div class="stat"><div class="label">PostgreSQL</div><div class="value"><span class="status-dot dot-` + dbClass + `"></span>` + dbStatus + `</div><div class="sub">Primary datastore</div></div>
<div class="stat"><div class="label">Redis</div><div class="value"><span class="status-dot dot-` + redisClass + `"></span>` + redisStatus + `</div><div class="sub">Streams MQ + Cache</div></div>
<div class="stat"><div class="label">Environment</div><div class="value">` + env + `</div><div class="sub">Go ` + goVer + `</div></div>
<div class="stat"><div class="label">Memory</div><div class="value">` + fmt.Sprint(memAlloc) + ` MB</div><div class="sub">Alloc / Sys</div></div>
<div class="stat"><div class="label">Goroutines</div><div class="value">` + fmt.Sprint(goroutines) + `</div><div class="sub">Active</div></div>
</div><div class="section"><h2>System Statistics <button class="refresh-btn" onclick="loadStats()">Refresh</button></h2><div id="stats-content" class="loading">Loading...</div></div></div>
<div id="tab-mae" class="tab"><div class="section"><h2>MAE Configurations <button class="refresh-btn" onclick="loadList('mae-list','mae-table')">Refresh</button></h2>
<table><thead><tr><th>MAE ID</th><th>Name</th><th>Type</th><th>Status</th><th>Version</th><th>Daily Vmax</th><th>Weight</th><th>Created</th></tr></thead><tbody id="mae-table"><tr><td colspan="8" class="loading">Loading...</td></tr></tbody></table></div></div>
<div id="tab-payout" class="tab"><div class="section"><h2>Payout Requests <button class="refresh-btn" onclick="loadList('payouts','payout-table')">Refresh</button></h2>
<table><thead><tr><th>ID</th><th>Merchant</th><th>Amount</th><th>Currency</th><th>Asset</th><th>Status</th><th>Version</th><th>Created</th></tr></thead><tbody id="payout-table"><tr><td colspan="8" class="loading">Loading...</td></tr></tbody></table></div></div>
<div id="tab-treasury" class="tab">
<div class="section"><h2>Banking Calendars <button class="refresh-btn" onclick="loadList('calendars','cal-table')">Refresh</button></h2>
<table><thead><tr><th>ID</th><th>Name</th><th>Jurisdiction</th><th>Timezone</th><th>Status</th><th>Created</th></tr></thead><tbody id="cal-table"><tr><td colspan="6" class="loading">Loading...</td></tr></tbody></table></div>
<div class="section"><h2>Sweep Rules <button class="refresh-btn" onclick="loadList('sweep-rules','rule-table')">Refresh</button></h2>
<table><thead><tr><th>ID</th><th>Currency</th><th>Rail</th><th>Beneficiary</th><th>Threshold</th><th>Max Daily</th><th>Mode</th><th>Status</th></tr></thead><tbody id="rule-table"><tr><td colspan="8" class="loading">Loading...</td></tr></tbody></table></div>
<div class="section"><h2>Sweep Transfers <button class="refresh-btn" onclick="loadList('sweep-transfers','transfer-table')">Refresh</button></h2>
<table><thead><tr><th>ID</th><th>Rule ID</th><th>Amount</th><th>Currency</th><th>Rail</th><th>Status</th><th>Bank Ref</th><th>Created</th></tr></thead><tbody id="transfer-table"><tr><td colspan="8" class="loading">Loading...</td></tr></tbody></table></div></div>
<div id="tab-webhook" class="tab"><div class="section"><h2>Webhook Events <button class="refresh-btn" onclick="loadList('webhook-events','wh-table')">Refresh</button></h2>
<table><thead><tr><th>ID</th><th>Provider Type</th><th>Provider ID</th><th>Event Type</th><th>Status</th><th>Created</th></tr></thead><tbody id="wh-table"><tr><td colspan="6" class="loading">Loading...</td></tr></tbody></table></div></div>
<div id="tab-ledger" class="tab"><div class="section"><h2>Ledger Transactions <button class="refresh-btn" onclick="loadList('ledger/transactions','txn-table')">Refresh</button></h2>
<table><thead><tr><th>ID</th><th>Idempotency Key</th><th>Type</th><th>Aggregate</th><th>Description</th><th>Created</th></tr></thead><tbody id="txn-table"><tr><td colspan="6" class="loading">Loading...</td></tr></tbody></table></div></div>
<div id="tab-outbox" class="tab"><div class="section"><h2>Outbox Events <button class="refresh-btn" onclick="loadList('outbox','outbox-table')">Refresh</button></h2>
<table><thead><tr><th>ID</th><th>Aggregate</th><th>Event Type</th><th>Status</th><th>Retry</th><th>Created</th></tr></thead><tbody id="outbox-table"><tr><td colspan="6" class="loading">Loading...</td></tr></tbody></table></div></div>
<div id="tab-merchants" class="tab"><div class="section"><h2>Merchant Ledger <button class="refresh-btn" onclick="loadList('merchants','merchant-table')">Refresh</button></h2>
<table><thead><tr><th>Merchant ID</th><th>Currency</th><th>Settled</th><th>Frozen</th><th>Reserve</th><th>Dispute</th><th>Version</th><th>Updated</th></tr></thead><tbody id="merchant-table"><tr><td colspan="8" class="loading">Loading...</td></tr></tbody></table></div></div>
<div id="tab-endpoints" class="tab"><div class="section"><h2>API Endpoints</h2><div class="card"><pre>
<span class="method m-get">GET</span>  /healthz                              Health check
<span class="method m-get">GET</span>  /readyz                               Readiness check
<span class="method m-get">GET</span>  /admin/dashboard                      Dashboard HTML
<span class="method m-get">GET</span>  /admin/stats                          System statistics
<span class="method m-get">GET</span>  /admin/mae-list                       List MAE configs
<span class="method m-get">GET</span>  /admin/mae-detail/{mae_id}            Get MAE detail
<span class="method m-post">POST</span> /admin/mae                            Create MAE
<span class="method m-patch">PATCH</span>/admin/mae/{mae_id}                  Update MAE
<span class="method m-post">POST</span> /admin/mae/{mae_id}/status           Change status
<span class="method m-get">GET</span>  /admin/mae/{mae_id}/metrics           Get metrics
<span class="method m-get">GET</span>  /admin/payouts                        List payouts
<span class="method m-get">GET</span>  /admin/payout/{id}                    Get payout
<span class="method m-post">POST</span> /merchant/payout-request              Create payout
<span class="method m-post">POST</span> /internal/payout/{id}/freeze         Freeze balance
<span class="method m-get">GET</span>  /admin/calendars                      List calendars
<span class="method m-get">GET</span>  /admin/sweep-rules                    List rules
<span class="method m-get">GET</span>  /admin/sweep-transfers                List transfers
<span class="method m-get">GET</span>  /admin/webhook-events                 List events
<span class="method m-get">GET</span>  /admin/merchant/{id}/balance           Balance
<span class="method m-get">GET</span>  /admin/merchants                      List merchants
<span class="method m-get">GET</span>  /admin/ledger/transactions            List transactions
<span class="method m-get">GET</span>  /admin/outbox                         List outbox
</pre></div></div></div>
<div id="tab-system" class="tab"><div class="section"><h2>System Information</h2><div class="card"><table>
<tr><td style="width:200px;color:var(--muted)">Go Version</td><td>` + goVer + `</td></tr>
<tr><td style="color:var(--muted)">CPU Cores</td><td>` + fmt.Sprint(cpu) + `</td></tr>
<tr><td style="color:var(--muted)">Goroutines</td><td>` + fmt.Sprint(goroutines) + `</td></tr>
<tr><td style="color:var(--muted)">Memory Alloc</td><td>` + fmt.Sprint(memAlloc) + ` MB</td></tr>
<tr><td style="color:var(--muted)">Memory Sys</td><td>` + fmt.Sprint(memSys) + ` MB</td></tr>
<tr><td style="color:var(--muted)">Uptime</td><td>` + uptime + `</td></tr>
<tr><td style="color:var(--muted)">Environment</td><td>` + env + `</td></tr>
<tr><td style="color:var(--muted)">PostgreSQL</td><td><span class="status-dot dot-` + dbClass + `"></span>` + dbStatus + `</td></tr>
<tr><td style="color:var(--muted)">Redis</td><td><span class="status-dot dot-` + redisClass + `"></span>` + redisStatus + `</td></tr>
</table></div></div></div>
</div>
<script>
var API_BASE='/admin';
function showTab(name,btn){document.querySelectorAll('.tab').forEach(function(t){t.classList.remove('active');});document.querySelectorAll('.nav button').forEach(function(b){b.classList.remove('active');});document.getElementById('tab-'+name).classList.add('active');btn.classList.add('active');if(name==='overview')loadStats();}
function loadStats(){fetch(API_BASE+'/stats',{headers:{'Authorization':'Bearer admin-token'}}).then(function(r){return r.json();}).then(function(d){var h='<div class="grid">';h+='<div class="stat"><div class="label">MAE Count</div><div class="value">'+(d.mae_count||0)+'</div></div>';h+='<div class="stat"><div class="label">Payout Requests</div><div class="value">'+(d.payout_count||0)+'</div></div>';h+='<div class="stat"><div class="label">Outbox Pending</div><div class="value">'+(d.outbox_pending||0)+'</div></div>';h+='<div class="stat"><div class="label">Webhook Events</div><div class="value">'+(d.webhook_count||0)+'</div></div>';h+='<div class="stat"><div class="label">Sweep Transfers</div><div class="value">'+(d.sweep_count||0)+'</div></div>';h+='<div class="stat"><div class="label">Merchants</div><div class="value">'+(d.merchant_count||0)+'</div></div>';h+='</div>';if(d.payout_by_status&&d.payout_by_status.length){h+='<h3 style="margin:16px 0 8px;color:var(--muted);font-size:13px">Payout Status Distribution</h3><div class="grid">';d.payout_by_status.forEach(function(s){h+='<div class="stat"><div class="label">'+s.status+'</div><div class="value">'+s.count+'</div></div>';});h+='</div>';}document.getElementById('stats-content').innerHTML=h;}).catch(function(){document.getElementById('stats-content').innerHTML='<div class="empty">Failed to load stats</div>';});}
function loadList(ep,tid){fetch(API_BASE+'/'+ep,{headers:{'Authorization':'Bearer admin-token'}}).then(function(r){return r.json();}).then(function(d){var items=d.items||[];if(!items.length){document.getElementById(tid).innerHTML='<tr><td colspan="10" class="empty">No data</td></tr>';return;}var h='';items.forEach(function(i){h+='<tr>';if(ep==='mae-list'){h+='<td>'+i.mae_id+'</td><td>'+i.display_name+'</td><td>'+i.mae_type+'</td><td><span class="badge badge-'+sb(i.status)+'">'+i.status+'</span></td><td>'+i.version+'</td><td>'+i.daily_vmax_minor+'</td><td>'+i.routing_weight+'</td><td>'+ft(i.created_at)+'</td>';}else if(ep==='payouts'){h+='<td>'+sh(i.id)+'</td><td>'+sh(i.merchant_id)+'</td><td>'+i.amount+'</td><td>'+i.fiat_currency+'</td><td>'+i.target_asset+'</td><td><span class="badge badge-'+sb(i.status)+'">'+i.status+'</span></td><td>'+i.version+'</td><td>'+ft(i.created_at)+'</td>';}else if(ep==='calendars'){h+='<td>'+sh(i.id)+'</td><td>'+i.calendar_name+'</td><td>'+i.jurisdiction+'</td><td>'+i.timezone+'</td><td><span class="badge badge-ok">'+i.status+'</span></td><td>'+ft(i.created_at)+'</td>';}else if(ep==='sweep-rules'){h+='<td>'+sh(i.id)+'</td><td>'+i.currency+'</td><td>'+i.rail+'</td><td>'+i.beneficiary_type+'</td><td>'+i.trigger_threshold_minor+'</td><td>'+i.max_daily_sweep_count+'</td><td>'+i.sweep_mode+'</td><td><span class="badge badge-ok">'+i.status+'</span></td>';}else if(ep==='sweep-transfers'){h+='<td>'+sh(i.id)+'</td><td>'+sh(i.sweep_rule_id)+'</td><td>'+i.amount_minor+'</td><td>'+i.currency+'</td><td>'+i.rail+'</td><td><span class="badge badge-'+sb(i.status)+'">'+i.status+'</span></td><td>'+i.bank_ref+'</td><td>'+ft(i.created_at)+'</td>';}else if(ep==='webhook-events'){h+='<td>'+sh(i.id)+'</td><td>'+i.provider_type+'</td><td>'+i.provider_id+'</td><td>'+i.event_type+'</td><td><span class="badge badge-'+sb(i.status)+'">'+i.status+'</span></td><td>'+ft(i.created_at)+'</td>';}else if(ep==='ledger/transactions'){h+='<td>'+sh(i.id)+'</td><td>'+i.idempotency_key+'</td><td>'+i.transaction_type+'</td><td>'+i.aggregate_type+'</td><td>'+i.description+'</td><td>'+ft(i.created_at)+'</td>';}else if(ep==='outbox'){h+='<td>'+sh(i.id)+'</td><td>'+i.aggregate_type+'</td><td>'+i.event_type+'</td><td><span class="badge badge-'+sb(i.status)+'">'+i.status+'</span></td><td>'+i.retry_count+'</td><td>'+ft(i.created_at)+'</td>';}else if(ep==='merchants'){h+='<td>'+sh(i.merchant_id)+'</td><td>'+i.currency+'</td><td>'+i.settled_balance+'</td><td>'+i.frozen_payout_balance+'</td><td>'+i.reserve_balance+'</td><td>'+i.dispute_hold_balance+'</td><td>'+i.version+'</td><td>'+ft(i.updated_at)+'</td>';}h+='</tr>';});document.getElementById(tid).innerHTML=h;}).catch(function(){document.getElementById(tid).innerHTML='<tr><td colspan="10" class="empty">Error loading data</td></tr>';});}
function sh(id){return id?String(id).substring(0,8)+'...':'-';}
function ft(t){return t?String(t).substring(0,19).replace('T',' '):'-';}
function sb(s){if(!s)return'info';s=s.toLowerCase();if(s==='active'||s==='confirmed'||s==='reconciled'||s==='completed'||s==='submitted')return'ok';if(s==='failed'||s==='rejected'||s==='deadletter')return'err';if(s==='cooldown'||s==='unknownexternalstate'||s==='pending')return'warn';return'info';}
loadStats();
</script></body></html>`
}
