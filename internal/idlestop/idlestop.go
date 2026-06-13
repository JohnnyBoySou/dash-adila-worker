// Package idlestop para containers gerenciados que ficaram rodando além do limite
// configurado. É o principal lever de custo da box Hetzner: container parado
// consome ~0 RAM e CPU — só o disco do volume é mantido.
package idlestop

import (
	"context"
	"log/slog"
	"time"

	"github.com/adila/dash/worker/internal/runtime"
)

// checkInterval é o intervalo entre cada varredura de idle. 5 minutos é granular
// o bastante para reagir a IdleStopAfter de dezenas de minutos sem overload.
const checkInterval = 5 * time.Minute

// Run bloqueia até ctx ser cancelado, varrendo a cada checkInterval. Deve ser
// chamado em goroutine própria (go idlestop.Run(...)).
func Run(ctx context.Context, rt runtime.ContainerRuntime, idleStopAfter time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check(ctx, rt, idleStopAfter, log)
		}
	}
}

// check é a lógica pura de uma varredura: lista os running, para os que
// ultrapassaram idleStopAfter desde StartedAt.
func check(ctx context.Context, rt runtime.ContainerRuntime, idleStopAfter time.Duration, log *slog.Logger) {
	instances, err := rt.ListRunning(ctx)
	if err != nil {
		log.Error("idle-stop: listar containers rodando", "err", err)
		return
	}
	now := time.Now()
	for _, inst := range instances {
		if inst.StartedAt.IsZero() {
			// Container sem StartedAt conhecido: ignorar (não parar por segurança).
			continue
		}
		idle := now.Sub(inst.StartedAt)
		if idle <= idleStopAfter {
			continue
		}
		if err := rt.Stop(ctx, inst.ID); err != nil {
			log.Error("idle-stop: parar container", "id", inst.ID, "err", err)
			continue
		}
		log.Info("idle-stop: container parado por inatividade",
			"id", inst.ID,
			"kind", string(inst.Kind),
			"idle_for", idle.Round(time.Second).String())
	}
}
