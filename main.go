// Command worker é o agent que roda na box Hetzner. Recebe HTTP do control plane
// e provisiona Postgres e Redis como containers Docker (um por tenant).
// Ver internal/api para o contrato HTTP e internal/runtime para a execução Docker.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adila/dash/worker/internal/api"
	"github.com/adila/dash/worker/internal/backup"
	"github.com/adila/dash/worker/internal/builder"
	"github.com/adila/dash/worker/internal/config"
	"github.com/adila/dash/worker/internal/idlestop"
	"github.com/adila/dash/worker/internal/proxy"
	"github.com/adila/dash/worker/internal/runtime"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("configuração inválida", "err", err)
		os.Exit(1)
	}

	rt := runtime.NewDocker(runtime.DockerConfig{
		Bin:                 cfg.DockerBin,
		BindHost:            cfg.BindHost,
		PortRangeStart:      cfg.PortRangeStart,
		PortRangeEnd:        cfg.PortRangeEnd,
		RedisPortRangeStart: cfg.RedisPortRangeStart,
		RedisPortRangeEnd:   cfg.RedisPortRangeEnd,
		AppPortRangeStart:   cfg.AppPortRangeStart,
		AppPortRangeEnd:     cfg.AppPortRangeEnd,
	})

	bd := builder.NewDocker(builder.DockerConfig{
		Bin:          cfg.DockerBin,
		BuilderImage: cfg.BuilderImage,
	})

	// Roteamento público dos apps: liga o CaddyRouter só quando AppsBaseDomain está
	// configurado; caso contrário o NoopRouter (default do Server) mantém o legado.
	var routerOpts []api.Option
	if cfg.RoutingEnabled() {
		log.Info("roteamento público de apps habilitado",
			"baseDomain", cfg.AppsBaseDomain, "caddyAppsDir", cfg.CaddyAppsDir)
		router := proxy.NewCaddyRouter(cfg.CaddyAppsDir, cfg.BindHost, cfg.CaddyBin, cfg.CaddyfilePath)
		routerOpts = append(routerOpts, api.WithRouter(router))
	}

	srv := api.NewServer(rt, bd, api.Config{
		Token:               cfg.Token,
		AdvertiseHost:       cfg.AdvertiseHost,
		SSLMode:             cfg.SSLMode,
		DefaultPgVersion:    cfg.DefaultPgVersion,
		DefaultRedisVersion: cfg.DefaultRedisVersion,
		AppsBaseDomain:      cfg.AppsBaseDomain,
	}, log, routerOpts...)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// `docker run` com pull de imagem fria pode levar dezenas de segundos.
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Idle-stop: para containers que ficaram rodando além do limite (lever de custo).
	if cfg.IdleStopAfter > 0 {
		log.Info("idle-stop habilitado", "after", cfg.IdleStopAfter)
		go idlestop.Run(ctx, rt, cfg.IdleStopAfter, log)
	}

	// Backup automático para R2 (só se BackupInterval > 0 e credenciais presentes).
	if cfg.BackupInterval > 0 && cfg.R2Enabled() {
		log.Info("backups automáticos habilitados",
			"interval", cfg.BackupInterval,
			"retention", cfg.BackupRetention,
			"bucket", cfg.R2Bucket)
		r2 := backup.R2Config{
			AccountID:       cfg.R2AccountID,
			Bucket:          cfg.R2Bucket,
			AccessKeyID:     cfg.R2AccessKeyID,
			SecretAccessKey: cfg.R2SecretAccessKey,
		}
		runner := backup.NewRunner(cfg.DockerBin, r2, cfg.BackupInterval, cfg.BackupRetention, log)
		go runner.Run(ctx, rt)
	}

	go func() {
		log.Info("agent escutando", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("servidor HTTP falhou", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("encerrando agent")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown forçado", "err", err)
		os.Exit(1)
	}
}
