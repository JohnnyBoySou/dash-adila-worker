package api

// DTOs do contrato HTTP do agent. Os nomes JSON espelham EXATAMENTE o cliente TS
// (src/providers/hetzner/agent-client.ts) — qualquer divergência quebra a integração.

// createResourceRequest é o corpo de POST /v1/resources.
// Espelha CreateResourceRequest do TS.
type createResourceRequest struct {
	Kind           string          `json:"kind"`           // "postgres" | "redis"
	IdempotencyKey string          `json:"idempotencyKey"` // dedupe de provision
	Name           string          `json:"name"`           // slug lógico do service
	Version        string          `json:"version"`        // tag da imagem (ex.: "16")
	Region         string          `json:"region"`
	Limits         *resourceLimits `json:"limits"`
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

// errorBody é o corpo de erro. O cliente TS lê `error` (fallback `message`).
type errorBody struct {
	Error string `json:"error"`
}
