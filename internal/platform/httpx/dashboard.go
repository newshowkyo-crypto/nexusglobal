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

type DashboardData struct {
	System    SystemInfo `json:"system"`
	Database  DBInfo     `json:"database"`
	Redis     RedisInfo  `json:"redis"`
	API       APIInfo    `json:"api"`
	Timestamp string     `json:"timestamp"`
}

type SystemInfo struct {
	GoVersion    string `json:"go_version"`
	NumCPU       int    `json:"num_cpu"`
	NumGoroutine int    `json:"num_goroutine"`
	MemAlloc     uint64 `json:"mem_alloc_mb"`
	MemSys       uint64 `json:"mem_sys_mb"`
}

type DBInfo struct {
	Connected     bool `json:"connected"`
	TotalConns    int  `json:"total_conns"`
	AcquiredConns int  `json:"acquired_conns"`
	IdleConns     int  `json:"idle_conns"`
	MaxConns      int  `json:"max_conns"`
}

type RedisInfo struct {
	Connected bool `json:"connected"`
}

type APIInfo struct {
	Version string `json:"version"`
	Env     string `json:"env"`
	Uptime  string `json:"uptime"`
}

var startTime = time.Now()

func DashboardHandler(pool *pgxpool.Pool, rdb *redis.Client, env string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		dbInfo := DBInfo{Connected: false}
		if pool != nil {
			stat := pool.Stat()
			dbInfo = DBInfo{
				Connected:     true,
				TotalConns:    int(stat.TotalConns()),
				AcquiredConns: int(stat.AcquiredConns()),
				IdleConns:     int(stat.IdleConns()),
				MaxConns:      int(stat.MaxConns()),
			}
		}

		redisInfo := RedisInfo{Connected: false}
		if rdb != nil {
			if err := rdb.Ping(r.Context()).Err(); err == nil {
				redisInfo.Connected = true
			}
		}

		data := DashboardData{
			System: SystemInfo{
				GoVersion:    runtime.Version(),
				NumCPU:       runtime.NumCPU(),
				NumGoroutine: runtime.NumGoroutine(),
				MemAlloc:     mem.Alloc / 1024 / 1024,
				MemSys:       mem.Sys / 1024 / 1024,
			},
			Database: dbInfo,
			Redis:    redisInfo,
			API: APIInfo{
				Version: "1.0.0",
				Env:     env,
				Uptime:  time.Since(startTime).Round(time.Second).String(),
			},
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	}
}

func AdminDashboardHTML(pool *pgxpool.Pool, rdb *redis.Client, env string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		dbStatus := "disconnected"
		dbClass := "status-error"
		dbConns := "N/A"
		if pool != nil {
			stat := pool.Stat()
			dbStatus = "connected"
			dbClass = "status-ok"
			dbConns = fmt.Sprintf("%d/%d", stat.AcquiredConns(), stat.MaxConns())
		}

		redisStatus := "disconnected"
		redisClass := "status-error"
		if rdb != nil {
			if err := rdb.Ping(r.Context()).Err(); err == nil {
				redisStatus = "connected"
				redisClass = "status-ok"
			}
		}

		uptime := time.Since(startTime).Round(time.Second)

		html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>NexusGlobal MAMR - Admin Dashboard</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #0f172a; color: #e2e8f0; min-height: 100vh; }
.header { background: linear-gradient(135deg, #1e293b 0%%, #334155 100%%); padding: 24px 32px; border-bottom: 1px solid #334155; }
.header h1 { font-size: 24px; font-weight: 700; color: #f8fafc; }
.header p { color: #94a3b8; margin-top: 4px; font-size: 14px; }
.container { max-width: 1200px; margin: 0 auto; padding: 24px; }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 20px; margin-bottom: 24px; }
.card { background: #1e293b; border-radius: 12px; padding: 24px; border: 1px solid #334155; transition: transform 0.2s; }
.card:hover { transform: translateY(-2px); border-color: #475569; }
.card-title { font-size: 13px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px; color: #64748b; margin-bottom: 12px; }
.card-value { font-size: 28px; font-weight: 700; color: #f8fafc; }
.card-sub { font-size: 13px; color: #94a3b8; margin-top: 8px; }
.status-ok { color: #22c55e; }
.status-error { color: #ef4444; }
.status-badge { display: inline-block; padding: 4px 12px; border-radius: 20px; font-size: 12px; font-weight: 600; }
.badge-ok { background: #052e16; color: #22c55e; border: 1px solid #166534; }
.badge-error { background: #2a0a0a; color: #ef4444; border: 1px solid #7f1d1d; }
.section-title { font-size: 18px; font-weight: 600; color: #f8fafc; margin-bottom: 16px; padding-bottom: 8px; border-bottom: 1px solid #334155; }
.endpoint-table { width: 100%%; border-collapse: collapse; }
.endpoint-table th { text-align: left; padding: 12px 16px; background: #1e293b; color: #64748b; font-size: 12px; text-transform: uppercase; letter-spacing: 0.5px; border-bottom: 1px solid #334155; }
.endpoint-table td { padding: 12px 16px; border-bottom: 1px solid #1e293b; font-size: 14px; }
.endpoint-table tr:hover td { background: #1e293b; }
.method { display: inline-block; padding: 2px 8px; border-radius: 4px; font-size: 11px; font-weight: 700; font-family: monospace; min-width: 50px; text-align: center; }
.method-get { background: #052e16; color: #22c55e; }
.method-post { background: #172554; color: #3b82f6; }
.method-patch { background: #422006; color: #f59e0b; }
.path { font-family: 'SF Mono', 'Fira Code', monospace; color: #e2e8f0; }
.footer { text-align: center; padding: 24px; color: #475569; font-size: 12px; margin-top: 40px; }
</style>
</head>
<body>
<div class="header">
  <h1>NexusGlobal MAMR System</h1>
  <p>Enterprise Treasury Management & Multi-Acquirer Routing &mdash; Admin Dashboard</p>
</div>
<div class="container">
  <div class="grid">
    <div class="card">
      <div class="card-title">System Status</div>
      <div class="card-value"><span class="status-ok">&#9679;</span> Online</div>
      <div class="card-sub">Uptime: %s | Env: %s</div>
    </div>
    <div class="card">
      <div class="card-title">PostgreSQL</div>
      <div class="card-value"><span class="%s">&#9679;</span> %s</div>
      <div class="card-sub">Connections: %s</div>
    </div>
    <div class="card">
      <div class="card-title">Redis</div>
      <div class="card-value"><span class="%s">&#9679;</span> %s</div>
      <div class="card-sub">Streams MQ + Cache</div>
    </div>
    <div class="card">
      <div class="card-title">Runtime</div>
      <div class="card-value">%s</div>
      <div class="card-sub">Go %s | %d CPU | %d goroutines</div>
    </div>
    <div class="card">
      <div class="card-title">Memory</div>
      <div class="card-value">%d MB</div>
      <div class="card-sub">Alloc: %d MB | Sys: %d MB</div>
    </div>
    <div class="card">
      <div class="card-title">API Version</div>
      <div class="card-value">v1.0.0</div>
      <div class="card-sub">Production Ready</div>
    </div>
  </div>

  <div class="section-title">API Endpoints</div>
  <div class="card" style="overflow-x: auto;">
    <table class="endpoint-table">
      <thead>
        <tr><th>Method</th><th>Path</th><th>Description</th></tr>
      </thead>
      <tbody>
        <tr><td><span class="method method-get">GET</span></td><td class="path">/healthz</td><td>Health check (liveness)</td></tr>
        <tr><td><span class="method method-get">GET</span></td><td class="path">/readyz</td><td>Readiness check (PG + Redis)</td></tr>
        <tr><td><span class="method method-get">GET</span></td><td class="path">/admin/dashboard</td><td>This dashboard</td></tr>
        <tr><td><span class="method method-get">GET</span></td><td class="path">/admin/dashboard/data</td><td>Dashboard JSON data</td></tr>
        <tr><td colspan="3" style="padding: 8px 0; border: none;"></td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/admin/mae</td><td>Create MAE configuration</td></tr>
        <tr><td><span class="method method-patch">PATCH</span></td><td class="path">/admin/mae/{id}</td><td>Update MAE configuration</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/admin/mae/{id}/rotate-secret</td><td>Rotate MAE credentials</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/admin/mae/{id}/status</td><td>Change MAE status</td></tr>
        <tr><td><span class="method method-get">GET</span></td><td class="path">/admin/mae/{id}/metrics</td><td>Get MAE metrics</td></tr>
        <tr><td colspan="3" style="padding: 8px 0; border: none;"></td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/admin/banking-calendar</td><td>Create banking calendar</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/admin/sweep-rule</td><td>Create sweep rule</td></tr>
        <tr><td><span class="method method-patch">PATCH</span></td><td class="path">/admin/sweep-rule/{id}</td><td>Update sweep rule</td></tr>
        <tr><td><span class="method method-get">GET</span></td><td class="path">/admin/webhook/events</td><td>List webhook events</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/admin/webhook/events/{id}/replay</td><td>Replay webhook event</td></tr>
        <tr><td colspan="3" style="padding: 8px 0; border: none;"></td></tr>
        <tr><td><span class="method method-get">GET</span></td><td class="path">/admin/merchant/{id}/balance</td><td>Query merchant balance</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/merchant/payout-address</td><td>Add payout address</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/merchant/payout-request</td><td>Create payout request</td></tr>
        <tr><td colspan="3" style="padding: 8px 0; border: none;"></td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/internal/payout/{id}/freeze</td><td>Freeze balance (Phase 1)</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/internal/payout/{id}/fx-lock</td><td>FX Lock (Phase 2)</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/internal/payout/{id}/submit</td><td>LP Payout (Phase 3)</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/internal/payout/{id}/reconcile</td><td>Reconcile payout</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/internal/sweep/evaluate</td><td>Evaluate sweep trigger</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/internal/sweep/{id}/submit</td><td>Submit sweep transfer</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/internal/sweep/{id}/reconcile</td><td>Reconcile sweep</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/internal/mae/{id}/circuit-break</td><td>Trigger MAE circuit break</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/internal/mae/{id}/testing-evaluate</td><td>Evaluate MAE testing</td></tr>
        <tr><td colspan="3" style="padding: 8px 0; border: none;"></td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/webhook/mae/{provider_id}</td><td>MAE webhook receiver</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/webhook/bank/{provider_id}</td><td>Bank webhook receiver</td></tr>
        <tr><td><span class="method method-post">POST</span></td><td class="path">/webhook/lp/{provider_id}</td><td>LP webhook receiver</td></tr>
      </tbody>
    </table>
  </div>

  <div class="section-title" style="margin-top: 32px;">Architecture</div>
  <div class="card">
    <pre style="color: #94a3b8; font-size: 13px; line-height: 1.6; overflow-x: auto;">
Webhook Receiver Proxy          Core API Server              Worker Process
┌─────────────────┐            ┌─────────────────┐         ┌─────────────────┐
│  TLS/IP Check   │            │  /admin/*       │         │  Outbox Worker  │
│  Signature Verify│   XADD    │  /merchant/*    │         │  FX Lock Worker │
│  Idempotency    │───────────>│  /internal/*    │         │  LP Payout Work │
│  Encrypt Raw    │  Streams   │  /webhook/*     │         │  Reconcile Work │
│  Write Ingress  │            │                 │         │                 │
└─────────────────┘            └────────┬────────┘         └────────┬────────┘
                                        │                           │
                                        ▼                           ▼
                               ┌─────────────────┐         ┌─────────────────┐
                               │  PostgreSQL 15   │         │   Redis 7       │
                               │  - Dual-Entry    │         │   - Streams MQ  │
                               │  - Row Locking   │         │   - Delay Queue │
                               │  - Idempotency   │         │   - Metrics     │
                               └─────────────────┘         └─────────────────┘
    </pre>
  </div>
</div>
<div class="footer">
  NexusGlobal MAMR System v1.0.0 &mdash; Enterprise Treasury Management
</div>
</body>
</html>`,
			uptime.String(), env,
			dbClass, dbStatus, dbConns,
			redisClass, redisStatus,
			uptime.String(),
			runtime.Version(), runtime.NumCPU(), runtime.NumGoroutine(),
			mem.Alloc/1024/1024,
			mem.Alloc/1024/1024, mem.Sys/1024/1024,
		)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	}
}
