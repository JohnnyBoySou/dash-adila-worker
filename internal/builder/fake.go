package builder

import (
	"context"
	"sync"
)

// Fake é um Builder em memória para testar a camada api sem Docker/kaniko. Modela o
// ciclo observável (running → succeeded/failed, 404 em build ausente) sem lançar
// container nenhum. Os testes controlam o desfecho via Complete/Fail.
type Fake struct {
	mu     sync.Mutex
	builds map[string]*Status // id → estado
	target map[string]string  // id → ImageTarget (para Complete remontar o ImageRef)

	// FailStart, se setado, faz Start devolver esse erro (injeção de falha).
	FailStart error
}

func NewFake() *Fake {
	return &Fake{
		builds: make(map[string]*Status),
		target: make(map[string]string),
	}
}

func (f *Fake) Start(_ context.Context, spec Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailStart != nil {
		return f.FailStart
	}
	// Build começa em running; o ImageRef só aparece quando concluído.
	f.builds[spec.ID] = &Status{
		ID:     spec.ID,
		Status: StatusRunning,
	}
	// Guarda o alvo para Complete remontar o ImageRef.
	f.target[spec.ID] = spec.ImageTarget
	return nil
}

func (f *Fake) Get(_ context.Context, id string) (*Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.builds[id]
	if !ok {
		return nil, nil
	}
	cp := *st
	return &cp, nil
}

func (f *Fake) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.builds, id)
	delete(f.target, id)
	return nil // idempotente
}

// Complete marca um build como succeeded com imageRef/digest (usado pelos testes
// para simular o desfecho do poll).
func (f *Fake) Complete(id, digest string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.builds[id]; ok {
		st.Status = StatusSucceeded
		st.ImageRef = f.target[id]
		st.Digest = digest
	}
}

// Fail marca um build como failed com logs (usado pelos testes).
func (f *Fake) Fail(id, logs string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.builds[id]; ok {
		st.Status = StatusFailed
		st.Logs = logs
	}
}
