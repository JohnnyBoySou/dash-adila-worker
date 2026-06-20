package idlestop

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/adila/dash/worker/internal/runtime"
)

func quietMonitor(rt runtime.ContainerRuntime, defaultIdle time.Duration) *monitor {
	return &monitor{
		rt:          rt,
		defaultIdle: defaultIdle,
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		state:       make(map[string]*activity),
	}
}

// makeServerless cria uma instância serverless rodando no fake, com NetRx inicial.
func makeServerless(t *testing.T, fake *runtime.Fake, id string, idleSeconds int, netRx int64) {
	t.Helper()
	_, err := fake.Create(context.Background(), runtime.Spec{
		ID:                 id,
		IdempotencyKey:     "key-" + id,
		Kind:               runtime.KindPostgres,
		Serverless:         true,
		IdleTimeoutSeconds: idleSeconds,
	})
	if err != nil {
		t.Fatalf("criar instância: %v", err)
	}
	fake.SetMetrics(id, &runtime.Metrics{ID: id, Kind: runtime.KindPostgres, NetRxBytes: netRx})
}

func statusOf(t *testing.T, fake *runtime.Fake, id string) string {
	t.Helper()
	inst, err := fake.Get(context.Background(), id)
	if err != nil || inst == nil {
		t.Fatalf("get %s: inst=%v err=%v", id, inst, err)
	}
	return inst.Status
}

func TestScanStopsIdleServerlessContainer(t *testing.T) {
	ctx := context.Background()
	fake := runtime.NewFake()
	makeServerless(t, fake, "pg-1", 0, 1000)
	m := quietMonitor(fake, 0) // janela 0 → qualquer NetRx parado para na 2ª varredura

	m.scan(ctx) // baseline: nunca para na primeira observação
	if got := statusOf(t, fake, "pg-1"); got != "running" {
		t.Fatalf("após baseline: status = %q, quer running", got)
	}

	fake.SetMetrics("pg-1", &runtime.Metrics{ID: "pg-1", NetRxBytes: 1000}) // sem atividade
	m.scan(ctx)
	if got := statusOf(t, fake, "pg-1"); got != "stopped" {
		t.Fatalf("inativo: status = %q, quer stopped", got)
	}
}

func TestScanKeepsActiveContainer(t *testing.T) {
	ctx := context.Background()
	fake := runtime.NewFake()
	makeServerless(t, fake, "pg-1", 0, 1000)
	m := quietMonitor(fake, 0)

	m.scan(ctx)                                                             // baseline NetRx=1000
	fake.SetMetrics("pg-1", &runtime.Metrics{ID: "pg-1", NetRxBytes: 5000}) // houve tráfego
	m.scan(ctx)
	if got := statusOf(t, fake, "pg-1"); got != "running" {
		t.Fatalf("com atividade: status = %q, quer running", got)
	}
}

func TestScanIgnoresProvisionedContainer(t *testing.T) {
	ctx := context.Background()
	fake := runtime.NewFake()
	_, err := fake.Create(ctx, runtime.Spec{
		ID:             "pg-prov",
		IdempotencyKey: "k",
		Kind:           runtime.KindPostgres,
		Serverless:     false, // provisioned: nunca parado pelo idle-stop
	})
	if err != nil {
		t.Fatalf("criar: %v", err)
	}
	m := quietMonitor(fake, 0)
	m.scan(ctx)
	m.scan(ctx)
	if got := statusOf(t, fake, "pg-prov"); got != "running" {
		t.Fatalf("provisioned: status = %q, quer running (intocado)", got)
	}
}

func TestScanRespectsPerContainerIdleTimeout(t *testing.T) {
	ctx := context.Background()
	fake := runtime.NewFake()
	makeServerless(t, fake, "pg-1", 3600, 1000) // janela própria de 1h
	m := quietMonitor(fake, 0)                  // default seria 0, mas o container manda

	m.scan(ctx)
	fake.SetMetrics("pg-1", &runtime.Metrics{ID: "pg-1", NetRxBytes: 1000}) // parado
	m.scan(ctx)
	// idle real é ~0s, bem abaixo da janela de 1h → não para.
	if got := statusOf(t, fake, "pg-1"); got != "running" {
		t.Fatalf("dentro da janela: status = %q, quer running", got)
	}
}

func TestScanForgetsStoppedContainers(t *testing.T) {
	ctx := context.Background()
	fake := runtime.NewFake()
	makeServerless(t, fake, "pg-1", 0, 1000)
	m := quietMonitor(fake, 0)

	m.scan(ctx)
	fake.SetMetrics("pg-1", &runtime.Metrics{ID: "pg-1", NetRxBytes: 1000})
	m.scan(ctx) // para o container
	if _, ok := m.state["pg-1"]; ok {
		t.Fatalf("estado de pg-1 deveria ter sido esquecido após o stop")
	}
}
