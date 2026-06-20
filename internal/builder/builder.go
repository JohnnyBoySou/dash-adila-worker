// Package builder transforma código-fonte de um repositório git numa imagem OCI,
// publicando-a num registry. O build roda num container EFÊMERO e ISOLADO (kaniko,
// userspace, sem daemon) — código de tenant não-confiável nunca toca o socket do
// Docker nem outros containers. O resto do agent (camada api) depende só da
// interface Builder; a implementação concreta (kaniko via CLI do Docker) é injetada,
// o que permite testar a camada HTTP com um fake em memória.
package builder

import (
	"context"
	"errors"
)

// ErrNotFound sinaliza que o build pedido não existe. A camada api traduz para
// HTTP 404 — o adapter TS trata como build desconhecido (poll encerra).
var ErrNotFound = errors.New("build não encontrado")

// Estados semânticos de um build, devolvidos ao control plane. Espelham o enum
// BuildStatus do schema TS (QUEUED→RUNNING→SUCCEEDED/FAILED).
const (
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// RegistryAuth são as credenciais de push para o registry de destino. O container
// de build as usa para autenticar o push da imagem produzida.
type RegistryAuth struct {
	Server   string // host do registry (ex.: registry.interno:5000)
	Username string
	Password string
}

// Spec descreve um build source → imagem, em termos de domínio. O ID já vem gerado
// pela camada api (estável → vira nome/label do container de build).
type Spec struct {
	ID             string
	IdempotencyKey string
	Name           string // slug lógico do service (label informativo)

	// Origem (git):
	RepoCloneURL string // https://github.com/owner/repo.git (SEM token embutido)
	GitToken     string // token efêmero do GitHub App; injetado no clone pelo container
	Ref          string // branch ou commit a buildar (ex.: "main", "deploy")
	CommitSha    string // commit exato (opcional; informativo)

	// Destino (registry):
	ImageTarget  string // registry/repo:tag de destino do push
	RegistryAuth RegistryAuth

	// Build:
	Dockerfile string // caminho relativo do Dockerfile no repo (vazio = nixpacks autodetecta)

	// Limites do container de build (0 = sem limite). Barram um build adversarial de
	// esgotar a box.
	MemoryMb int
	CPUs     float64
}

// Status é o estado observável de um build. O container de build é a fonte da
// verdade (status via inspect, logs via docker logs) — o agent não guarda estado.
type Status struct {
	ID       string
	Status   string // running | succeeded | failed
	ImageRef string // imagem produzida (== Spec.ImageTarget; vazio enquanto running)
	Digest   string // sha256:... quando o kaniko reporta (só em succeeded)
	Logs     string // stdout/stderr combinados do build (best-effort)
}

// Builder é o contrato que a camada api usa. Implementações: Docker (kaniko real)
// e Fake (testes).
type Builder interface {
	// Start lança o build de forma ASSÍNCRONA (container detached) e devolve sem
	// esperar a conclusão — um build a frio leva minutos, acima do WriteTimeout do
	// HTTP. O control plane faz poll via Get até o estado terminal.
	Start(ctx context.Context, spec Spec) error
	// Get inspeciona o build pelo id: status semântico + logs. (nil, nil) se não existe.
	Get(ctx context.Context, id string) (*Status, error)
	// Delete remove o container de build (idempotente: não-existe → nil). O control
	// plane chama após consumir o resultado terminal.
	Delete(ctx context.Context, id string) error
}
