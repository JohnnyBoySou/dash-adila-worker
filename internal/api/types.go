package api

// DTOs do contrato HTTP do agent. Os nomes JSON espelham EXATAMENTE o cliente TS
// (src/providers/hetzner/agent-client.ts) — qualquer divergência quebra a integração.

// createResourceRequest é o corpo de POST /v1/resources.
// Espelha CreateResourceRequest do TS.
type createResourceRequest struct {
	Kind           string          `json:"kind"`           // "postgres" | "redis" | "app"
	IdempotencyKey string          `json:"idempotencyKey"` // dedupe de provision
	Name           string          `json:"name"`           // slug lógico do service
	Version        string          `json:"version"`        // tag da imagem (ex.: "16")
	Region         string          `json:"region"`
	Limits         *resourceLimits `json:"limits"`
	// Serverless marca o recurso para scale-to-zero por inatividade (idle-stop).
	Serverless bool `json:"serverless"`
	// IdleTimeoutSeconds é a janela de inatividade antes do stop (0 = default do agent).
	IdleTimeoutSeconds int `json:"idleTimeoutSeconds"`
	// App-specific (kind=app):
	Image         string            `json:"image"`         // imagem Docker completa (obrigatório para kind=app)
	Env           map[string]string `json:"env"`           // variáveis de ambiente
	ContainerPort int               `json:"containerPort"` // porta do container (default 8080)
	Command       []string          `json:"command"`       // override CMD
	Volumes       []volumeMountReq  `json:"volumes"`       // discos persistentes a montar
}

// volumeMountReq é um disco persistente declarado no deploy de um app. ID é o id
// estável do volume no control plane (vira o nome do volume Docker); MountPath é o
// caminho absoluto dentro do container. Espelha o item de `volumes` no TS.
type volumeMountReq struct {
	ID        string `json:"id"`
	MountPath string `json:"mountPath"`
}

// setDomainsRequest é o corpo de PUT /v1/resources/{id}/domains. Define a lista
// COMPLETA de domínios próprios do app (replace); o subdomínio padrão é sempre
// mantido pelo agent. Lista vazia limpa os domínios custom.
type setDomainsRequest struct {
	Domains []string `json:"domains"`
}

// resourceLimits espelha AgentResourceLimits do TS.
type resourceLimits struct {
	MemoryMb int     `json:"memoryMb"`
	CPUs     float64 `json:"cpus"`
	DiskGb   int     `json:"diskGb"`
}

// resourceConnection espelha AgentConnection. `URL` é segredo (string de conexão pronta).
type resourceConnection struct {
	URL      string `json:"url"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Database string `json:"database,omitempty"`
	Username string `json:"username,omitempty"`
}

// resourceResponse espelha AgentResource: o estado que o agent devolve ao control plane.
type resourceResponse struct {
	ID         string              `json:"id"`
	Kind       string              `json:"kind"`
	Status     string              `json:"status"` // semântico (running/stopped/crashed/...)
	Region     string              `json:"region,omitempty"`
	Connection *resourceConnection `json:"connection,omitempty"`
	Metadata   map[string]any      `json:"metadata,omitempty"`
}

// metricsResponse é o corpo de GET /v1/resources/{id}/metrics. Espelha AgentMetrics do
// TS. Snapshot de uso (gauges) num instante — não há segredo aqui.
type metricsResponse struct {
	ID               string  `json:"id"`
	Kind             string  `json:"kind"`
	Status           string  `json:"status"`
	CollectedAt      string  `json:"collectedAt"` // RFC3339
	CPUPercent       float64 `json:"cpuPercent"`
	MemoryBytes      int64   `json:"memoryBytes"`
	MemoryLimitBytes int64   `json:"memoryLimitBytes,omitempty"`
	NetRxBytes       int64   `json:"netRxBytes"`
	NetTxBytes       int64   `json:"netTxBytes"`
	DiskBytes        int64   `json:"diskBytes,omitempty"`
	Keys             int64   `json:"keys,omitempty"`
	UptimeSeconds    int64   `json:"uptimeSeconds"`
}

// createBuildRequest é o corpo de POST /v1/builds. Espelha CreateBuildRequest do TS
// (agent-client.ts). Dispara um build source → imagem num container kaniko isolado.
type createBuildRequest struct {
	IdempotencyKey string          `json:"idempotencyKey"` // dedupe de build
	Name           string          `json:"name"`           // slug lógico do service
	RepoCloneURL   string          `json:"repoCloneUrl"`   // https://github.com/owner/repo.git (SEM token)
	GitToken       string          `json:"gitToken"`       // token efêmero do GitHub App (injetado no clone)
	Ref            string          `json:"ref"`            // branch ou commit a buildar
	CommitSha      string          `json:"commitSha"`      // commit exato (opcional)
	ImageTarget    string          `json:"imageTarget"`    // registry/repo:tag de destino do push
	Dockerfile     string          `json:"dockerfile"`     // caminho do Dockerfile (vazio = nixpacks autodetecta)
	Registry       registryAuthDTO `json:"registry"`       // credenciais de push
	Limits         *resourceLimits `json:"limits"`         // limites do container de build
}

// registryAuthDTO espelha AgentRegistryAuth do TS. Credenciais de push para o registry.
type registryAuthDTO struct {
	Server   string `json:"server"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// buildResponse é o corpo de POST /v1/builds (202) e GET /v1/builds/{id}. Espelha
// AgentBuild do TS — o estado que o control plane consulta em poll até o terminal.
type buildResponse struct {
	ID       string `json:"id"`
	Status   string `json:"status"`             // running | succeeded | failed
	ImageRef string `json:"imageRef,omitempty"` // imagem produzida (só em succeeded)
	Digest   string `json:"digest,omitempty"`   // sha256:... (só em succeeded)
	Logs     string `json:"logs,omitempty"`     // saída do build (best-effort)
}

// logLineResponse é uma linha de log de runtime. Espelha AgentLogLine do TS.
type logLineResponse struct {
	Timestamp string `json:"timestamp,omitempty"` // RFC3339Nano (UTC); vazio se desconhecido
	Message   string `json:"message"`
}

// logsResponse é o corpo de GET /v1/resources/{id}/logs. Espelha AgentLogs do TS —
// as linhas de log de runtime do workload (não há segredo; é a saída do container).
type logsResponse struct {
	Lines []logLineResponse `json:"lines"`
}

// errorBody é o corpo de erro. O cliente TS lê `error` (fallback `message`).
type errorBody struct {
	Error string `json:"error"`
}
