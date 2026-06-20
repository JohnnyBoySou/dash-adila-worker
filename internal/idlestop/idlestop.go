// Package idlestop implementa o scale-to-zero por inatividade dos recursos marcados
// como serverless. É o principal lever de custo da box Hetzner: container parado
// consome ~0 RAM e CPU — só o disco do volume é mantido.
//
// O sinal de atividade é o contador de bytes recebidos pela rede (docker stats NetIO):
// um delta zero entre varreduras significa que ninguém falou com o container. Recursos
// "provisioned" (a maioria) não têm o label serverless e nunca são parados aqui.
package idlestop

import (
	"context"
	"log/slog"
	"time"

	"github.com/adila/dash/worker/internal/runtime"
)

// checkInterval é o intervalo entre cada varredura de idle. 5 minutos é granular
// o bastante para reagir a janelas de inatividade de dezenas de minutos sem overload.
const checkInterval = 5 * time.Minute

// activity rastreia o último sinal de rede observado de um container serverless e
// quando ele mudou pela última vez. Mantido em memória entre varreduras — reconstruído
// na primeira observação após um restart do agent (que nunca dispara stop imediato).
type activity struct {
	lastRxBytes  int64
	lastActiveAt time.Time
}

// Run bloqueia até ctx ser cancelado, varrendo a cada checkInterval. Deve ser chamado
// em goroutine própria (go idlestop.Run(...)). defaultIdleAfter é a janela aplicada
// quando o container não declara um idle.timeout próprio.
func Run(ctx context.Context, rt runtime.ContainerRuntime, defaultIdleAfter time.Duration, log *slog.Logger) {
	m := &monitor{rt: rt, defaultIdle: defaultIdleAfter, log: log, state: make(map[string]*activity)}
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.scan(ctx)
		}
	}
}

// monitor carrega o estado de atividade entre varreduras. Mantido fora de Run para
// que scan seja testável isoladamente.
type monitor struct {
	rt          runtime.ContainerRuntime
	defaultIdle time.Duration
	log         *slog.Logger
	state       map[string]*activity
}

// scan faz uma varredura: para cada container SERVERLESS rodando, compara o contador
// de rede com a varredura anterior. Sem variação por toda a janela de inatividade,
// para o container. Recursos provisioned (não-serverless) são ignorados.
func (m *monitor) scan(ctx context.Context) {
	instances, err := m.rt.ListRunning(ctx)
	if err != nil {
		m.log.Error("idle-stop: listar containers rodando", "err", err)
		return
	}
	now := time.Now()
	seen := make(map[string]bool, len(instances))
	for _, inst := range instances {
		if !inst.Serverless {
			continue // provisioned: nunca parado automaticamente
		}
		seen[inst.ID] = true

		idleAfter := m.defaultIdle
		if inst.IdleTimeoutSeconds > 0 {
			idleAfter = time.Duration(inst.IdleTimeoutSeconds) * time.Second
		}

		mx, err := m.rt.Metrics(ctx, inst.ID)
		if err != nil {
			m.log.Error("idle-stop: coletar métricas", "id", inst.ID, "err", err)
			continue
		}
		if mx == nil {
			continue // sumiu entre o list e o metrics
		}

		st := m.state[inst.ID]
		if st == nil {
			// Primeira observação: registra baseline e considera ativo agora. Garante
			// que nunca paramos na primeira varredura nem logo após um restart do agent.
			m.state[inst.ID] = &activity{lastRxBytes: mx.NetRxBytes, lastActiveAt: now}
			continue
		}
		if mx.NetRxBytes != st.lastRxBytes {
			// Houve tráfego desde a última varredura → marca como ativo agora.
			st.lastRxBytes = mx.NetRxBytes
			st.lastActiveAt = now
			continue
		}

		idle := now.Sub(st.lastActiveAt)
		if idle < idleAfter {
			continue
		}
		if err := m.rt.Stop(ctx, inst.ID); err != nil {
			m.log.Error("idle-stop: parar container", "id", inst.ID, "err", err)
			continue
		}
		m.log.Info("idle-stop: container parado por inatividade",
			"id", inst.ID,
			"kind", string(inst.Kind),
			"idle_for", idle.Round(time.Second).String())
		// Esquece o estado: ao religar, recomeça com baseline fresco.
		delete(m.state, inst.ID)
	}

	// Esquece containers que não estão mais rodando (parados/removidos) para o mapa
	// de estado não crescer indefinidamente.
	for id := range m.state {
		if !seen[id] {
			delete(m.state, id)
		}
	}
}
