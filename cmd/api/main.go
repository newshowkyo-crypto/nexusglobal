package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/nexusglobal/mamr/internal/ledger/handler"
	maehandler "github.com/nexusglobal/mamr/internal/mae/handler"
	payouthandler "github.com/nexusglobal/mamr/internal/payout/handler"
	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
	"github.com/nexusglobal/mamr/internal/platform/logger"
	treasuryhandler "github.com/nexusglobal/mamr/internal/treasury/handler"
	webhookhandler "github.com/nexusglobal/mamr/internal/webhook/handler"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel)

	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(httpx.RequestID)
	r.Use(httpx.Recover(log))

	ledgerH := &handler.Handler{}
	maeH := maehandler.New()
	payoutH := payouthandler.New()
	webhookH := webhookhandler.New()
	treasuryH := treasuryhandler.New()

	noopPing := func() error { return nil }

	r.Get("/healthz", httpx.HealthCheck(noopPing, noopPing))
	r.Get("/readyz", httpx.ReadinessCheck(noopPing, noopPing))

	r.Route("/admin", func(r chi.Router) {
		r.Route("/merchant/{id}", func(r chi.Router) {
			r.Get("/balance", ledgerH.GetBalance)
		})
		r.Route("/mae", func(r chi.Router) {
			r.Post("/", maeH.Create)
			r.Patch("/{mae_id}", maeH.Update)
			r.Post("/{mae_id}/rotate-secret", maeH.RotateSecret)
			r.Post("/{mae_id}/status", maeH.ChangeStatus)
			r.Get("/{mae_id}/metrics", maeH.GetMetrics)
		})
		r.Route("/banking-calendar", func(r chi.Router) {
			r.Post("/", treasuryH.CreateCalendar)
		})
		r.Route("/sweep-rule", func(r chi.Router) {
			r.Post("/", treasuryH.CreateSweepRule)
			r.Patch("/{id}", treasuryH.UpdateSweepRule)
		})
		r.Get("/webhook/events", webhookH.ListEvents)
		r.Post("/webhook/events/{id}/replay", webhookH.Replay)
	})

	r.Route("/merchant", func(r chi.Router) {
		r.Post("/payout-address", payoutH.CreateAddress)
		r.Post("/payout-request", payoutH.CreatePayoutRequest)
	})

	r.Route("/internal", func(r chi.Router) {
		r.Post("/payout/{id}/freeze", payoutH.Freeze)
		r.Post("/sweep/evaluate", treasuryH.Evaluate)
		r.Post("/sweep/{id}/submit", treasuryH.Submit)
		r.Post("/sweep/{id}/reconcile", treasuryH.Reconcile)
		r.Post("/mae/{mae_id}/circuit-break", maeH.CircuitBreak)
		r.Post("/mae/{mae_id}/testing-evaluate", maeH.TestingEvaluate)
	})

	r.Route("/webhook", func(r chi.Router) {
		r.Post("/mae/{provider_id}", webhookH.HandleMAE)
		r.Post("/bank/{provider_id}", webhookH.HandleBank)
		r.Post("/lp/{provider_id}", webhookH.HandleLP)
	})

	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: r,
	}

	go func() {
		log.Info("API server starting", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	log.Info("server stopped")
}
