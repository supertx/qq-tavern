package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/supertx/qq-tavern/internal/config"
	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/eventbus"
	"github.com/supertx/qq-tavern/internal/model"
	"github.com/supertx/qq-tavern/internal/qq"
	"github.com/supertx/qq-tavern/internal/state"
	"github.com/supertx/qq-tavern/internal/worker"
)

var version = "0.2.1-dev"

func main() {
	if err := run(); err != nil {
		slog.Error("startup/runtime failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 2 {
		return errors.New("usage: qq-tavern gateway|worker|check|healthcheck|version")
	}
	role := os.Args[1]
	if role == "version" {
		fmt.Println(version)
		return nil
	}
	if role == "healthcheck" {
		addr := os.Getenv("LISTEN_ADDR")
		if addr == "" {
			addr = ":8080"
		}
		if strings.HasPrefix(addr, ":") {
			addr = "127.0.0.1" + addr
		}
		client := &http.Client{Timeout: 3 * time.Second}
		res, err := client.Get("http://" + addr + "/health/ready")
		if err != nil {
			return errors.New("health endpoint unavailable")
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			return errors.New("service not ready")
		}
		return nil
	}
	if role == "check" {
		role = os.Getenv("ROLE")
		if role == "" {
			role = "worker"
		}
		_, err := config.Load(role)
		if err == nil {
			fmt.Println("configuration valid for " + role)
		}
		return err
	}
	cfg, err := config.Load(role)
	if err != nil {
		return err
	}
	bus, err := eventbus.New(cfg.RedisURL, domain.Key(cfg.AppID))
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	startup, stop := context.WithTimeout(ctx, 10*time.Second)
	err = bus.Ping(startup)
	stop()
	if err != nil {
		return errors.New("Redis unavailable; check REDIS_URL")
	}
	var handler http.Handler
	var jobs sync.WaitGroup
	if role == "gateway" {
		g, err := qq.NewGateway(qq.Config{AppID: cfg.AppID, Secret: cfg.AppSecret, APIBase: cfg.QQAPIBase, TokenURL: cfg.QQTokenURL, InternalToken: cfg.InternalToken, DBPath: cfg.Database, ObserveGroupMessages: cfg.Observe}, bus)
		if err != nil {
			return err
		}
		defer g.Close()
		handler = g.Handler()
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			if err := g.Run(ctx); err != nil && ctx.Err() == nil {
				slog.Error("gateway stopped", "error_type", fmt.Sprintf("%T", err))
				cancel()
			}
		}()
	} else {
		store, err := state.Open(cfg.Database)
		if err != nil {
			return err
		}
		defer store.Close()
		provider, err := model.New(model.Config{BaseURL: cfg.ModelBase, APIKey: cfg.ModelKey, Mode: cfg.Mode}, nil)
		if err != nil {
			return err
		}
		service, err := worker.New(cfg, store, provider, qq.NewRemoteSender(cfg.GatewayURL, cfg.InternalToken))
		if err != nil {
			return err
		}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); fmt.Fprintln(w, "live") })
		mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
			check, done := context.WithTimeout(r.Context(), 2*time.Second)
			defer done()
			if bus.Ping(check) != nil || store.DB().PingContext(check) != nil {
				http.Error(w, "not ready", 503)
				return
			}
			fmt.Fprintln(w, "ready")
		})
		handler = mux
		jobs.Add(2)
		go func() { defer jobs.Done(); service.RunSummaries(ctx) }()
		go func() { defer jobs.Done(); consume(ctx, bus, service, cfg.MaxConcurrency) }()
	}
	server := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 300 * time.Second, IdleTimeout: 60 * time.Second}
	failed := make(chan error, 1)
	go func() { failed <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err = <-failed:
		if !errors.Is(err, http.ErrServerClosed) {
			cancel()
			jobs.Wait()
			return err
		}
	}
	cancel()
	end, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	_ = server.Shutdown(end)
	jobs.Wait()
	return nil
}
func consume(ctx context.Context, bus *eventbus.Bus, service *worker.Service, concurrency int) {
	consumer := fmt.Sprintf("worker-%d", os.Getpid())
	sem := make(chan struct{}, concurrency)
	var jobs sync.WaitGroup
	defer jobs.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		delivery, err := bus.Read(ctx, consumer)
		if err != nil {
			<-sem
			if ctx.Err() != nil {
				return
			}
			slog.Warn("queue read failed")
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		if delivery == nil {
			<-sem
			continue
		}
		jobs.Add(1)
		go func(d *eventbus.Delivery) {
			defer jobs.Done()
			defer func() { <-sem }()
			_ = service.Handle(ctx, d.Event)
			ack, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			if err := bus.Ack(ack, d.ID); err != nil {
				slog.Warn("queue ack failed")
			}
		}(delivery)
	}
}
