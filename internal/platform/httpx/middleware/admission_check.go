// Package middleware — admission_check.go 实现 P0-2 AdmissionCheck 中间件。
//
// 设计：
//   - AdmissionCheck 中间件在支付路由链最前段执行
//   - 调用 failclosed.Service.AdmissionCheck(ctx) 检查封锁状态
//   - 封锁时返回 HTTP 503 + SYSTEM_LOCKDOWN_FAIL_CLOSED
//   - DB 查询失败时 fail-closed（返回 503）
//
// 使用方式：
//
//	r.Route("/api/v3/checkout", func(r chi.Router) {
//	    r.Use(middleware.MerchantAuth(verifier))
//	    r.Use(middleware.AdmissionCheck(failClosedSvc))  // P0-2
//	    r.Post("/sessions", handler.CreateSession)
//	})
//
// 注意：此中间件不替代 handler 内部的 admissionCheck 调用，
// 而是作为路由级中间件提供早期拒绝（在 handler 之前）。
// handler 内部的 admissionCheck 是兜底，防止绕过中间件。
package middleware

import (
	"net/http"

	"github.com/nexusglobal/mamr/internal/platform/apperr"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
)

// AdmissionCheck 中间件：在支付路由链最前段执行封锁检查（P0-2）。
//
// 若 svc 为 nil，跳过检查（测试场景）。
// 否则调用 svc.AdmissionCheck(ctx)：
//   - 返回 nil：放行
//   - 返回 ErrSystemLockdown：HTTP 503 + SYSTEM_LOCKDOWN_FAIL_CLOSED
//
// 此中间件应放在 MerchantAuth 之后（需要 merchant context 用于日志），
// 但在 RateLimit 之前（封锁优先于限流）。
func AdmissionCheck(svc *failclosed.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if svc == nil {
				next.ServeHTTP(w, r)
				return
			}
			if err := svc.AdmissionCheck(r.Context()); err != nil {
				WriteAPIError(w, apperr.New(failclosed.SystemLockdownCode, http.StatusServiceUnavailable))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
