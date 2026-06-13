// Package api expõe o contrato HTTP do agent ao control plane. Depende só da
// interface runtime.ContainerRuntime, então é testável com um fake em memória.
package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/adila/dash/worker/internal/runtime"
)

// Limite do corpo de requisição. O maior DTO (createResourceRequest) tem poucas
// centenas de bytes; 64 KB é folga de sobra e barra um corpo adversarial gigante.
const maxBodyBytes = 64 << 10

// Validação dos campos que chegam do control plane e fluem para argumentos do
// Docker (nome/label/tag de imagem/filtro). exec é por slice (sem shell), então
// não há injeção de shell — mas um valor com `=`, espaço ou newline confundiria o
// parser de label/filtro do daemon e poderia casar containers errados. Allowlist
// estrita resolve na borda. (Achados HIGH-2/HIGH-3/LOW-10 do code review.)
var (
	// idempotencyKey: identificador opaco gerado pelo control plane.
	reIdempotencyKey = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
	// name/region: slugs lógicos; podem ser vazios.
	reSlug = regexp.MustCompile(`^[A-Za-z0-9_.-]{0,64}$`)
	// version: tag de imagem Docker (ex.: "16", "16.2-alpine").
	reImageTag = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// Config é a configuração que o server precisa (subconjunto da config.Config).
type Config struct {
	Token               string // bearer token do control plane
	AdvertiseHost       string // host que entra na connection URL
	SSLMode             string // ?sslmode= da connection URL
	DefaultPgVersion    string // versão Postgres usada quando o request omite
	DefaultRedisVersion string // versão Redis usada quando o request omite
}

// Server implementa os handlers HTTP do agent.
type Server struct {
	rt  runtime.ContainerRuntime
	cfg Config
	log *slog.Logger
}

func NewServer(rt runtime.ContainerRuntime, cfg Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{rt: rt, cfg: cfg, log: log}
}

// Handler monta o roteamento (Go 1.22+ ServeMux com método no padrão) e envolve
// tudo com a autenticação bearer.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth) // liveness, sem auth
	mux.HandleFunc("POST /v1/resources", s.requireAuth(s.handleCreate))
	mux.HandleFunc("GET /v1/resources/{id}", s.requireAuth(s.handleGet))
	mux.HandleFunc("POST /v1/resources/{id}/stop", s.requireAuth(s.handleStop))
	mux.HandleFunc("POST /v1/resources/{id}/start", s.requireAuth(s.handleStart))
	mux.HandleFunc("DELETE /v1/resources/{id}", s.requireAuth(s.handleDelete))
	return mux
}

// requireAuth valida o header Authorization: Bearer <token> em tempo constante.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) {
			writeError(w, http.StatusUnauthorized, "token ausente")
			return
		}
		given := strings.TrimPrefix(header, prefix)
		// subtle.ConstantTimeCompare evita timing attack na comparação do token.
		if subtle.ConstantTimeCompare([]byte(given), []byte(s.cfg.Token)) != 1 {
			writeError(w, http.StatusUnauthorized, "token inválido")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var req createResourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "corpo JSON inválido")
		return
	}
	if !reIdempotencyKey.MatchString(req.IdempotencyKey) {
		writeError(w, http.StatusBadRequest, "idempotencyKey ausente ou em formato inválido")
		return
	}
	if !reSlug.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "name em formato inválido")
		return
	}
	if !reSlug.MatchString(req.Region) {
		writeError(w, http.StatusBadRequest, "region em formato inválido")
		return
	}
	if req.Kind == "" {
		req.Kind = string(runtime.KindPostgres)
	}
	kind := runtime.Kind(req.Kind)
	if kind != runtime.KindPostgres && kind != runtime.KindRedis {
		writeError(w, http.StatusBadRequest, "kind não suportado: "+req.Kind)
		return
	}
	version := req.Version
	if version == "" {
		if kind == runtime.KindRedis {
			version = s.cfg.DefaultRedisVersion
		} else {
			version = s.cfg.DefaultPgVersion
		}
	}
	if !reImageTag.MatchString(version) {
		writeError(w, http.StatusBadRequest, "version (tag de imagem) em formato inválido")
		return
	}

	spec := runtime.Spec{
		ID:             newResourceID(kind),
		IdempotencyKey: req.IdempotencyKey,
		Kind:           kind,
		Name:           req.Name,
		Version:        version,
		Region:         req.Region,
	}
	if req.Limits != nil {
		spec.MemoryMb = req.Limits.MemoryMb
		spec.CPUs = req.Limits.CPUs
	}

	inst, err := s.rt.Create(r.Context(), spec)
	if err != nil {
		s.fail(w, "create", err)
		return
	}
	writeJSON(w, http.StatusCreated, s.toResponse(inst))
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inst, err := s.rt.Get(r.Context(), id)
	if err != nil {
		s.fail(w, "get", err)
		return
	}
	if inst == nil {
		writeError(w, http.StatusNotFound, "recurso não encontrado")
		return
	}
	writeJSON(w, http.StatusOK, s.toResponse(inst))
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.lifecycle(w, r, s.rt.Stop)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	s.lifecycle(w, r, s.rt.Start)
}

// lifecycle fatora stop/start: ambos recebem o id, traduzem ErrNotFound→404 e
// respondem 202 sem corpo (o control plane faz poll via GET para o status novo).
func (s *Server) lifecycle(w http.ResponseWriter, r *http.Request, op func(context.Context, string) error) {
	id := r.PathValue("id")
	if err := op(r.Context(), id); err != nil {
		if errors.Is(err, runtime.ErrNotFound) {
			writeError(w, http.StatusNotFound, "recurso não encontrado")
			return
		}
		s.fail(w, "lifecycle", err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// destroyData=true por padrão; só false quando explicitamente "false".
	destroyData := r.URL.Query().Get("destroyData") != "false"
	if err := s.rt.Delete(r.Context(), id, destroyData); err != nil {
		s.fail(w, "delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toResponse mapeia a instância do runtime para o DTO, montando a connection URL.
func (s *Server) toResponse(inst *runtime.Instance) resourceResponse {
	resp := resourceResponse{
		ID:     inst.ID,
		Kind:   string(inst.Kind),
		Status: inst.Status,
		Region: inst.Region,
		Metadata: map[string]any{
			"hetznerAgent": true,
			"name":         inst.Name,
		},
	}
	if inst.HostPort > 0 {
		switch inst.Kind {
		case runtime.KindRedis:
			resp.Connection = &resourceConnection{
				URL:  s.buildRedisURL(inst),
				Host: s.cfg.AdvertiseHost,
				Port: inst.HostPort,
			}
		default: // Postgres
			if inst.User != "" {
				resp.Connection = &resourceConnection{
					URL:      s.buildConnectionURL(inst),
					Host:     s.cfg.AdvertiseHost,
					Port:     inst.HostPort,
					Database: inst.Database,
					Username: inst.User,
				}
			}
		}
	}
	return resp
}

// buildConnectionURL monta postgresql://user:pass@host:port/db?sslmode=… com
// net/url, que escapa usuário/senha corretamente (sem montagem manual de string).
func (s *Server) buildConnectionURL(inst *runtime.Instance) string {
	u := url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(inst.User, inst.Password),
		Host:   s.cfg.AdvertiseHost + ":" + strconv.Itoa(inst.HostPort),
		Path:   "/" + inst.Database,
	}
	q := url.Values{}
	q.Set("sslmode", s.cfg.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

// buildRedisURL monta redis://:pass@host:port/0 via net/url.
// Username vazio + password = url.UserPassword("", pass) → ":pass@".
func (s *Server) buildRedisURL(inst *runtime.Instance) string {
	u := url.URL{
		Scheme: "redis",
		User:   url.UserPassword("", inst.Password),
		Host:   s.cfg.AdvertiseHost + ":" + strconv.Itoa(inst.HostPort),
		Path:   "/0",
	}
	return u.String()
}

// fail loga o erro completo no servidor e responde 500 com mensagem genérica —
// não vaza detalhe interno (stderr do Docker pode conter caminhos/segredos).
func (s *Server) fail(w http.ResponseWriter, op string, err error) {
	s.log.Error("operação falhou", "op", op, "err", err)
	writeError(w, http.StatusInternalServerError, "erro interno ao processar "+op)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// newResourceID gera um id único para o recurso. O prefixo identifica o kind no
// nome do container Docker (adila-<id>) e em logs.
func newResourceID(kind runtime.Kind) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand falhou: %v", err))
	}
	suffix := hex.EncodeToString(b)
	if kind == runtime.KindRedis {
		return "redis-" + suffix
	}
	return "pg-" + suffix
}
