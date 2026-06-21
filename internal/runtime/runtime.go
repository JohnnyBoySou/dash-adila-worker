// Package runtime abstrai o provisionamento de recursos gerenciados (Postgres,
// Redis) como containers numa box. O resto do agent (camada api) depende só da
// interface ContainerRuntime — a implementação concreta (Docker via CLI) é
// injetada, o que permite testar a camada HTTP com um fake em memória.
package runtime

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound sinaliza que o recurso pedido não existe. A camada api traduz para
// HTTP 404 — que o adapter TS (HetznerDatabaseProvider) trata como no-op idempotente
// em suspend/resume/delete.
var ErrNotFound = errors.New("recurso não encontrado")

// Kind é o tipo de recurso gerenciado.
type Kind string

const (
	KindPostgres Kind = "postgres"
	KindRedis    Kind = "redis"
	KindApp      Kind = "app"
)

// Spec descreve o recurso a provisionar, em termos de domínio (sem detalhes de Docker).
// O ID já vem gerado pela camada api (estável → vira nome/label do container).
type Spec struct {
	ID             string
	IdempotencyKey string
	Kind           Kind
	Name           string // slug lógico do service (vira label informativo)
	Version        string // tag da imagem (ex.: "16" → postgres:16, "7" → redis:7)
	Region         string
	MemoryMb       int     // 0 = sem limite
	CPUs           float64 // 0 = sem limite

	// Serverless marca o recurso para scale-to-zero por inatividade: o idle-stop
	// para o container quando não há tráfego de rede dentro da janela. A maioria
	// dos recursos é "provisioned" (Serverless=false) e nunca é parada automaticamente.
	Serverless bool
	// IdleTimeoutSeconds é a janela de inatividade antes do stop quando Serverless.
	// 0 = usa o default do agent (cfg.IdleStopAfter).
	IdleTimeoutSeconds int

	// App-specific (Kind == KindApp):
	Image         string            // imagem Docker completa (ex: ghcr.io/user/repo:sha)
	Env           map[string]string // variáveis de ambiente injetadas no container
	ContainerPort int               // porta que o container expõe (default 8080)
	Command       []string          // override do CMD da imagem (opcional)
}

// Instance é o estado observável de um recurso. O status é SEMÂNTICO — distingue
// parada intencional (stopped) de crash (crashed). Os campos de conexão são
// remontados a partir dos labels do container (o Docker é a fonte da verdade).
type Instance struct {
	ID             string
	IdempotencyKey string
	Kind           Kind
	Name           string
	Region         string
	Status         string // semântico: running/starting/stopped/crashed/unhealthy/...
	HostPort       int    // porta publicada no host (0 se ainda não atribuída)
	User           string // Postgres: "app"; Redis: vazio
	Password       string
	Database       string    // Postgres: "app"; Redis: vazio (usa índice 0)
	StartedAt      time.Time // quando o container foi iniciado pela última vez (zero = desconhecido)
	AppURL         string    // URL pública do app (apenas KindApp)

	// Serverless e IdleTimeoutSeconds são remontados dos labels — o idle-stop os lê
	// para decidir o que parar (e com qual janela) sem guardar estado próprio.
	Serverless         bool
	IdleTimeoutSeconds int
}

// Metrics é uma amostra de uso de um container num instante. É a fonte do pull
// periódico de métricas (control plane) e do sinal de atividade do idle-stop.
// Os contadores de rede servem de proxy de atividade: NetRxBytes sem variação
// entre varreduras significa que ninguém falou com o container.
type Metrics struct {
	ID               string
	Kind             Kind
	Status           string  // status semântico no momento da coleta
	CPUPercent       float64 // uso de CPU (% — pode passar de 100 com múltiplos cores)
	MemoryBytes      int64   // RAM usada (bytes)
	MemoryLimitBytes int64   // limite de RAM (bytes; 0 = sem limite explícito)
	NetRxBytes       int64   // bytes recebidos pela interface do container (sinal de atividade)
	NetTxBytes       int64   // bytes transmitidos
	DiskBytes        int64   // Postgres: pg_database_size; 0 = desconhecido/não aplicável
	Keys             int64   // Redis: DBSIZE; 0 = não aplicável
	UptimeSeconds    int64   // tempo desde StartedAt (0 se parado/desconhecido)
	CollectedAt      time.Time
}

// LogOptions parametriza a coleta de logs de runtime de um container.
type LogOptions struct {
	// Tail é o máximo de linhas (a partir do fim) a devolver. 0 = default do agent.
	Tail int
	// Since corta as linhas anteriores a este instante. Zero = sem corte temporal.
	Since time.Time
}

// LogLine é uma linha de log de runtime já separada do carimbo de tempo do daemon.
type LogLine struct {
	// Timestamp é o instante da linha (do carimbo `--timestamps`); zero se não parseável.
	Timestamp time.Time
	// Message é o conteúdo da linha sem o carimbo de tempo.
	Message string
}

// ContainerRuntime é o contrato que a camada api usa. Implementações: Docker (real)
// e Fake (testes).
type ContainerRuntime interface {
	// Create provisiona o recurso. É IDEMPOTENTE pela Spec.IdempotencyKey: se já
	// existe um recurso com a mesma key, devolve o existente sem criar outro.
	Create(ctx context.Context, spec Spec) (*Instance, error)
	// Get devolve a instância pelo id, ou (nil, nil) se não existe.
	Get(ctx context.Context, id string) (*Instance, error)
	// Stop para o recurso sem destruir os dados. ErrNotFound se não existe.
	Stop(ctx context.Context, id string) error
	// Start religa um recurso parado. ErrNotFound se não existe.
	Start(ctx context.Context, id string) error
	// Delete destrói o recurso (e o volume, se destroyData). Idempotente: não-existe → nil.
	Delete(ctx context.Context, id string, destroyData bool) error
	// ListRunning devolve todos os containers gerenciados com status running.
	// Usado pelo idle-stop e pelo backup para encontrar alvos.
	ListRunning(ctx context.Context) ([]*Instance, error)
	// Metrics coleta uma amostra de uso do container, ou (nil, nil) se não existe.
	// Best-effort: campos que dependem de exec (DiskBytes/Keys) ficam zerados quando
	// a coleta falha, sem que o método inteiro falhe.
	Metrics(ctx context.Context, id string) (*Metrics, error)
	// Logs coleta as linhas de log de runtime (stdout+stderr) do container, da mais
	// antiga para a mais recente, limitadas por opts. ErrNotFound se não existe.
	Logs(ctx context.Context, id string, opts LogOptions) ([]LogLine, error)
}
