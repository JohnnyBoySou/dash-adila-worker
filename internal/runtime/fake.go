package runtime

import (
	"context"
	"sync"
	"time"
)

// Fake é uma ContainerRuntime em memória para testar a camada api sem Docker.
// Modela o ciclo de vida observável (status semântico, idempotência por key,
// 404 em recurso ausente) sem subir container nenhum.
type Fake struct {
	mu        sync.Mutex
	instances map[string]*Instance // id → instância
	byKey     map[string]string    // idempotencyKey → id
	nextPort  int

	// FailCreate, se setado, faz Create devolver esse erro (injeção de falha nos testes).
	FailCreate error

	// metrics guarda overrides por id para Metrics(). Sem override, sintetiza valores
	// determinísticos. Usado para controlar o sinal de atividade nos testes do idle-stop.
	metrics map[string]*Metrics
}

func NewFake() *Fake {
	return &Fake{
		instances: make(map[string]*Instance),
		byKey:     make(map[string]string),
		nextPort:  54320,
		metrics:   make(map[string]*Metrics),
	}
}

func (f *Fake) Create(_ context.Context, spec Spec) (*Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.FailCreate != nil {
		return nil, f.FailCreate
	}

	// Idempotência por key: devolve o existente.
	if id, ok := f.byKey[spec.IdempotencyKey]; ok {
		return clone(f.instances[id]), nil
	}

	f.nextPort++
	inst := &Instance{
		ID:                 spec.ID,
		IdempotencyKey:     spec.IdempotencyKey,
		Kind:               spec.Kind,
		Name:               spec.Name,
		Region:             spec.Region,
		Status:             statusRunning,
		HostPort:           f.nextPort,
		Password:           "fake-secret",
		StartedAt:          time.Now(),
		Serverless:         spec.Serverless,
		IdleTimeoutSeconds: spec.IdleTimeoutSeconds,
	}
	// Campos de conexão variam por kind.
	if spec.Kind == KindPostgres {
		inst.User = "app"
		inst.Database = "app"
	}
	// Redis: User e Database ficam vazios — a URL é redis://:pass@host:port/0.

	f.instances[spec.ID] = inst
	f.byKey[spec.IdempotencyKey] = spec.ID
	return clone(inst), nil
}

func (f *Fake) Get(_ context.Context, id string) (*Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.instances[id]
	if !ok {
		return nil, nil
	}
	return clone(inst), nil
}

func (f *Fake) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.instances[id]
	if !ok {
		return ErrNotFound
	}
	inst.Status = statusStopped
	return nil
}

func (f *Fake) Start(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.instances[id]
	if !ok {
		return ErrNotFound
	}
	inst.Status = statusRunning
	return nil
}

func (f *Fake) Delete(_ context.Context, id string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if inst, ok := f.instances[id]; ok {
		delete(f.byKey, inst.IdempotencyKey)
		delete(f.instances, id)
	}
	return nil // idempotente: não-existe → nil
}

// ListRunning devolve todas as instâncias com status running. Usado pelo idle-stop
// e backup nos testes.
func (f *Fake) ListRunning(_ context.Context) ([]*Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []*Instance
	for _, inst := range f.instances {
		if inst.Status == statusRunning {
			result = append(result, clone(inst))
		}
	}
	return result, nil
}

// SetMetrics injeta um override de métricas para um id (usado pelos testes do idle-stop
// para simular variação — ou ausência — do sinal de atividade entre varreduras).
func (f *Fake) SetMetrics(id string, m *Metrics) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *m
	f.metrics[id] = &cp
}

// Metrics devolve o override registrado para o id, ou um snapshot sintético derivado
// da instância. (nil, nil) se o recurso não existe — espelhando Get.
func (f *Fake) Metrics(_ context.Context, id string) (*Metrics, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.metrics[id]; ok {
		cp := *m
		return &cp, nil
	}
	inst, ok := f.instances[id]
	if !ok {
		return nil, nil
	}
	return &Metrics{
		ID:            inst.ID,
		Kind:          inst.Kind,
		Status:        inst.Status,
		CPUPercent:    1.5,
		MemoryBytes:   32 * 1024 * 1024,
		NetRxBytes:    1024,
		NetTxBytes:    512,
		UptimeSeconds: 60,
		CollectedAt:   time.Now(),
	}, nil
}

func clone(in *Instance) *Instance {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}
