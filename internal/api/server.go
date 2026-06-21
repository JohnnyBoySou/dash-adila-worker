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
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/adila/dash/worker/internal/builder"
	"github.com/adila/dash/worker/internal/proxy"
	"github.com/adila/dash/worker/internal/runtime"
)

// Limite do corpo de requisição. O maior DTO (createResourceRequest) tem poucas
// centenas de bytes; 64 KB é folga de sobra e barra um corpo adversarial gigante.
const maxBodyBytes = 64 << 10

// maxIdleTimeoutSeconds limita a janela de inatividade do serverless a 24h — barra um
// valor absurdo que efetivamente desabilitaria o scale-to-zero.
const maxIdleTimeoutSeconds = 24 * 60 * 60

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
	// image (kind=app): referência completa (ex.: "ghcr.io/user/repo:sha", "nginx:1.27").
	// Allowlist sem espaço/`=`/`;`/newline — mesma defesa de borda dos outros campos.
	reImageRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,255}$`)
	// env key (kind=app): nome de variável de ambiente estilo shell. O VALOR é livre
	// (pode ser PEM multilinha, JSON, etc.) — exec por slice torna "KEY=VALUE" seguro.
	reEnvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

	// customDomain: domínio próprio que o usuário aponta para a box (ex.: "lp.adila.co",
	// "exemplo.com"). Rótulos DNS minúsculos separados por ponto, ≥2 rótulos. Mesma
	// defesa de borda do AppsBaseDomain — o domínio compõe um bloco do Caddyfile.
	reCustomDomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

	// --- Validação de borda dos campos de build (POST /v1/builds). Mesma defesa: tudo
	// que flui para args do `docker run` do container kaniko ou para o entrypoint do
	// builder passa por allowlist estrita (sem espaço/`;`/newline). ---

	// repoCloneUrl: URL https de clone (sem token embutido — o token vai à parte).
	reCloneURL = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?/[A-Za-z0-9._/-]{1,200}\.git$`)
	// gitToken: token efêmero do GitHub App (ex.: ghs_...). Allowlist conservadora.
	reGitToken = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,255}$`)
	// ref: branch ou commit; refs git permitem `/`, `.`, `-`, `_`.
	reGitRef = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)
	// commitSha: hash git (curto ou completo).
	reCommitSha = regexp.MustCompile(`^[a-f0-9]{7,40}$`)
	// dockerfile: caminho relativo do Dockerfile no repo.
	reDockerfilePath = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
)

// Config é a configuração que o server precisa (subconjunto da config.Config).
type Config struct {
	Token               string // bearer token do control plane
	AdvertiseHost       string // host que entra na connection URL
	SSLMode             string // ?sslmode= da connection URL
	DefaultPgVersion    string // versão Postgres usada quando o request omite
	DefaultRedisVersion string // versão Redis usada quando o request omite
	// AppsBaseDomain liga o roteamento público dos apps: vazio = desligado
	// (URL loopback, comportamento legado); preenchido (ex.: "apps.adila.co") faz
	// cada app ganhar o domínio <id>.<AppsBaseDomain> e a URL pública vira https://.
	AppsBaseDomain string
	// AppsPublicIP é o IP público da box para onde os domínios custom devem apontar.
	// Quando preenchido, ao definir um domínio custom o agent exige que o DNS dele
	// resolva para este IP (barra registrar rota que o Caddy não conseguiria emitir
	// cert via ACME). Vazio = só exige que o domínio resolva para algum endereço.
	AppsPublicIP string
}

// Server implementa os handlers HTTP do agent.
type Server struct {
	rt     runtime.ContainerRuntime
	bd     builder.Builder
	cfg    Config
	router proxy.Router
	log    *slog.Logger
	// resolveHost resolve um host em endereços IP. Injetável para teste; em produção
	// usa o resolver do sistema. Usado na pré-validação de DNS dos domínios custom.
	resolveHost func(ctx context.Context, host string) ([]string, error)
}

// Option configura o Server na construção (functional options).
type Option func(*Server)

// WithRouter injeta o roteador de proxy usado para publicar apps. Sem ele, o
// Server usa um NoopRouter (roteamento desligado). Ignora um router nil.
func WithRouter(r proxy.Router) Option {
	return func(s *Server) {
		if r != nil {
			s.router = r
		}
	}
}

func NewServer(rt runtime.ContainerRuntime, bd builder.Builder, cfg Config, log *slog.Logger, opts ...Option) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{rt: rt, bd: bd, cfg: cfg, router: proxy.NoopRouter{}, log: log,
		resolveHost: net.DefaultResolver.LookupHost}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Handler monta o roteamento (Go 1.22+ ServeMux com método no padrão) e envolve
// tudo com a autenticação bearer.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth) // liveness, sem auth
	mux.HandleFunc("POST /v1/resources", s.requireAuth(s.handleCreate))
	mux.HandleFunc("GET /v1/resources/{id}", s.requireAuth(s.handleGet))
	mux.HandleFunc("GET /v1/resources/{id}/metrics", s.requireAuth(s.handleMetrics))
	mux.HandleFunc("GET /v1/resources/{id}/logs", s.requireAuth(s.handleLogs))
	mux.HandleFunc("POST /v1/resources/{id}/stop", s.requireAuth(s.handleStop))
	mux.HandleFunc("POST /v1/resources/{id}/start", s.requireAuth(s.handleStart))
	mux.HandleFunc("DELETE /v1/resources/{id}", s.requireAuth(s.handleDelete))
	// Domínios custom de um app: define a lista completa de domínios próprios que
	// passam a rotear para o app (além do subdomínio padrão), com TLS automático.
	mux.HandleFunc("PUT /v1/resources/{id}/domains", s.requireAuth(s.handleSetDomains))
	// Builds (source → imagem) rodam de forma assíncrona: POST lança e devolve 202;
	// o control plane faz poll em GET até o estado terminal e remove via DELETE.
	mux.HandleFunc("POST /v1/builds", s.requireAuth(s.handleCreateBuild))
	mux.HandleFunc("GET /v1/builds/{id}", s.requireAuth(s.handleGetBuild))
	mux.HandleFunc("DELETE /v1/builds/{id}", s.requireAuth(s.handleDeleteBuild))
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
	spec, errMsg := s.specFromRequest(req)
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}

	inst, err := s.rt.Create(r.Context(), spec)
	if err != nil {
		s.fail(w, "create", err)
		return
	}

	// Publica o app no Caddy do host (se o roteamento estiver ligado). Falhar aqui
	// retorna 500 de propósito: o control plane re-tenta e tanto Create quanto Upsert
	// são idempotentes, então o retry converge — melhor que responder 201 com uma URL
	// pública que ainda não roteia.
	if inst.Kind == runtime.KindApp && inst.HostPort > 0 {
		if domain := s.appDomain(inst); domain != "" {
			if err := s.router.Upsert(r.Context(), inst.ID, []string{domain}, inst.HostPort); err != nil {
				s.fail(w, "route", err)
				return
			}
		}
	}

	writeJSON(w, http.StatusCreated, s.toResponse(r.Context(), inst))
}

// specFromRequest valida o corpo e monta o Spec; devolve uma mensagem de erro
// (string vazia = ok) que a camada HTTP transforma em 400. A validação diverge por
// kind: postgres/redis usam tag de imagem (version); app usa imagem completa, env
// e porta do container.
func (s *Server) specFromRequest(req createResourceRequest) (runtime.Spec, string) {
	if !reIdempotencyKey.MatchString(req.IdempotencyKey) {
		return runtime.Spec{}, "idempotencyKey ausente ou em formato inválido"
	}
	if !reSlug.MatchString(req.Name) {
		return runtime.Spec{}, "name em formato inválido"
	}
	if !reSlug.MatchString(req.Region) {
		return runtime.Spec{}, "region em formato inválido"
	}
	if req.Kind == "" {
		req.Kind = string(runtime.KindPostgres)
	}
	kind := runtime.Kind(req.Kind)
	if kind != runtime.KindPostgres && kind != runtime.KindRedis && kind != runtime.KindApp {
		return runtime.Spec{}, "kind não suportado: " + req.Kind
	}

	if req.IdleTimeoutSeconds < 0 || req.IdleTimeoutSeconds > maxIdleTimeoutSeconds {
		return runtime.Spec{}, "idleTimeoutSeconds fora do intervalo (0-86400)"
	}

	spec := runtime.Spec{
		ID:                 newResourceID(kind),
		IdempotencyKey:     req.IdempotencyKey,
		Kind:               kind,
		Name:               req.Name,
		Region:             req.Region,
		Serverless:         req.Serverless,
		IdleTimeoutSeconds: req.IdleTimeoutSeconds,
	}
	if req.Limits != nil {
		spec.MemoryMb = req.Limits.MemoryMb
		spec.CPUs = req.Limits.CPUs
	}

	if kind == runtime.KindApp {
		if msg := applyAppSpec(&spec, req); msg != "" {
			return runtime.Spec{}, msg
		}
		return spec, ""
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
		return runtime.Spec{}, "version (tag de imagem) em formato inválido"
	}
	spec.Version = version
	return spec, ""
}

// applyAppSpec valida e preenche os campos específicos de kind=app no Spec, ou
// devolve uma mensagem de erro de validação (string vazia = ok).
func applyAppSpec(spec *runtime.Spec, req createResourceRequest) string {
	if !reImageRef.MatchString(req.Image) {
		return "image ausente ou em formato inválido"
	}
	if req.ContainerPort < 0 || req.ContainerPort > 65535 {
		return "containerPort fora do intervalo (0-65535)"
	}
	for k := range req.Env {
		if !reEnvKey.MatchString(k) {
			return "env contém nome de variável inválido: " + k
		}
	}
	spec.Image = req.Image
	spec.Env = req.Env
	spec.ContainerPort = req.ContainerPort
	spec.Command = req.Command
	return ""
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
	writeJSON(w, http.StatusOK, s.toResponse(r.Context(), inst))
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := s.rt.Metrics(r.Context(), id)
	if err != nil {
		s.fail(w, "metrics", err)
		return
	}
	if m == nil {
		writeError(w, http.StatusNotFound, "recurso não encontrado")
		return
	}
	writeJSON(w, http.StatusOK, toMetricsResponse(m))
}

// maxLogTail limita o `tail` aceito na query — espelha o teto do runtime, rejeitando
// na borda um pedido absurdo antes de tocar o Docker.
const maxLogTailQuery = 2000

// handleLogs devolve as linhas de log de runtime do workload (poll sob demanda pela
// interface). 404 se o recurso não existe. Os parâmetros de query `tail` (nº de
// linhas) e `since` (RFC3339) são opcionais e validados na borda.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	opts, errMsg := logOptionsFromQuery(r.URL.Query())
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	lines, err := s.rt.Logs(r.Context(), id, opts)
	if err != nil {
		if errors.Is(err, runtime.ErrNotFound) {
			writeError(w, http.StatusNotFound, "recurso não encontrado")
			return
		}
		s.fail(w, "logs", err)
		return
	}
	writeJSON(w, http.StatusOK, toLogsResponse(lines))
}

// logOptionsFromQuery parseia e valida os parâmetros de query de logs. Devolve as
// opções e uma mensagem de erro (string vazia = ok) que a camada HTTP vira 400.
func logOptionsFromQuery(q url.Values) (runtime.LogOptions, string) {
	var opts runtime.LogOptions
	if raw := q.Get("tail"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > maxLogTailQuery {
			return opts, fmt.Sprintf("tail inválido (inteiro entre 0 e %d)", maxLogTailQuery)
		}
		opts.Tail = n
	}
	if raw := q.Get("since"); raw != "" {
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return opts, "since inválido (esperado RFC3339)"
		}
		opts.Since = ts
	}
	return opts, ""
}

// toLogsResponse mapeia as linhas do runtime para o DTO. Carimbos zero (não parseáveis)
// viram string vazia (omitida no JSON), nunca o "zero time" 0001-01-01.
func toLogsResponse(lines []runtime.LogLine) logsResponse {
	out := make([]logLineResponse, 0, len(lines))
	for _, l := range lines {
		var ts string
		if !l.Timestamp.IsZero() {
			ts = l.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, logLineResponse{Timestamp: ts, Message: l.Message})
	}
	return logsResponse{Lines: out}
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
	// Remove a rota pública do app (idempotente; no-op se o roteamento estiver
	// desligado ou o id não for de um app). O prefixo "app-" evita um reload do
	// Caddy à toa quando se deleta um Postgres/Redis.
	if strings.HasPrefix(id, "app-") {
		if err := s.router.Remove(r.Context(), id); err != nil {
			s.fail(w, "delete-route", err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxCustomDomains limita quantos domínios próprios um app pode ter — bound no
// tamanho do fragmento e no número de certs ACME que um único app dispara.
const maxCustomDomains = 10

// maxDomainLength é o teto de um nome DNS (RFC 1035): 253 octetos.
const maxDomainLength = 253

// dnsResolveTimeout limita a pré-validação de DNS dos domínios custom.
const dnsResolveTimeout = 5 * time.Second

// handleSetDomains define a lista COMPLETA de domínios custom de um app (semântica de
// replace: o que vier no corpo passa a ser o conjunto de domínios próprios; o
// subdomínio padrão <id>.<base> é sempre mantido). Lista vazia limpa os customs.
// Cada domínio é validado (formato, não pode estar sob o base-domain, sem duplicata)
// e pré-checado no DNS antes de registrar a rota — evita o Caddy martelar ACME num
// domínio que ainda não aponta para a box.
func (s *Server) handleSetDomains(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var req setDomainsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "corpo JSON inválido")
		return
	}

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
	if inst.Kind != runtime.KindApp {
		writeError(w, http.StatusBadRequest, "domínios custom só se aplicam a apps")
		return
	}

	// O subdomínio padrão é a âncora da rota; sem roteamento ligado não há onde anexar.
	base := s.appDomain(inst)
	if base == "" {
		writeError(w, http.StatusConflict, "roteamento público desligado neste agent")
		return
	}
	if inst.HostPort == 0 {
		writeError(w, http.StatusConflict, "app sem porta publicada; suba o app antes de definir domínios")
		return
	}

	customs, msg := s.normalizeCustomDomains(req.Domains, base)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	// Pré-valida o DNS de cada domínio custom: 422 se ainda não aponta para a box.
	// Cada domínio tem seu próprio orçamento de timeout — um resolver lento num
	// domínio não rouba o tempo dos demais (HIGH-1 do review).
	for _, d := range customs {
		ctx, cancel := context.WithTimeout(r.Context(), dnsResolveTimeout)
		msg := s.checkDomainDNS(ctx, d)
		cancel()
		if msg != "" {
			writeError(w, http.StatusUnprocessableEntity, msg)
			return
		}
	}

	// Reescreve a rota com o subdomínio padrão + os customs (idempotente).
	domains := append([]string{base}, customs...)
	if err := s.router.Upsert(r.Context(), inst.ID, domains, inst.HostPort); err != nil {
		s.fail(w, "set-domains", err)
		return
	}
	writeJSON(w, http.StatusOK, s.toResponse(r.Context(), inst))
}

// normalizeCustomDomains valida e normaliza a lista de domínios custom (lowercase,
// formato DNS, não pode estar sob o base-domain gerenciado, sem duplicata, dentro do
// limite). Devolve a lista limpa e uma mensagem de erro (string vazia = ok). Lista de
// entrada vazia é válida e significa "limpar os domínios custom".
func (s *Server) normalizeCustomDomains(in []string, base string) ([]string, string) {
	if len(in) > maxCustomDomains {
		return nil, fmt.Sprintf("máximo de %d domínios custom por app", maxCustomDomains)
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		d := strings.ToLower(strings.TrimSpace(raw))
		if d == "" {
			return nil, "domínio custom vazio"
		}
		if len(d) > maxDomainLength || !reCustomDomain.MatchString(d) {
			return nil, "domínio custom em formato inválido: " + d
		}
		// Domínios sob o base-domain da plataforma são gerenciados pelo agent (wildcard
		// + subdomínio padrão); aceitá-los como "custom" colidiria com essa gestão.
		if d == s.cfg.AppsBaseDomain || strings.HasSuffix(d, "."+s.cfg.AppsBaseDomain) {
			return nil, "domínio sob " + s.cfg.AppsBaseDomain + " é gerenciado pela plataforma: " + d
		}
		if d == base {
			return nil, "domínio custom não pode repetir o subdomínio padrão: " + d
		}
		if _, dup := seen[d]; dup {
			return nil, "domínio custom duplicado: " + d
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out, ""
}

// checkDomainDNS resolve o domínio e confere se ele aponta para a box. Devolve uma
// mensagem de erro (string vazia = ok). Com AppsPublicIP configurado, exige que um
// dos endereços resolvidos seja esse IP; sem ele, exige apenas que o domínio resolva.
func (s *Server) checkDomainDNS(ctx context.Context, domain string) string {
	addrs, err := s.resolveHost(ctx, domain)
	if err != nil || len(addrs) == 0 {
		return "o domínio " + domain + " não resolve no DNS; aponte-o para a box antes de adicioná-lo"
	}
	if s.cfg.AppsPublicIP == "" {
		return ""
	}
	if slices.Contains(addrs, s.cfg.AppsPublicIP) {
		return ""
	}
	return fmt.Sprintf("o domínio %s não aponta para a box (%s); ajuste o registro A no seu DNS", domain, s.cfg.AppsPublicIP)
}

// handleCreateBuild valida o corpo, lança o build assíncrono e responde 202 com o
// id + status running. NÃO espera a conclusão: um build a frio leva minutos, acima
// do WriteTimeout do HTTP. O control plane faz poll via GET /v1/builds/{id}.
func (s *Server) handleCreateBuild(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var req createBuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "corpo JSON inválido")
		return
	}
	spec, errMsg := buildSpecFromRequest(req)
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	if err := s.bd.Start(r.Context(), spec); err != nil {
		s.fail(w, "build", err)
		return
	}
	writeJSON(w, http.StatusAccepted, buildResponse{ID: spec.ID, Status: builder.StatusRunning})
}

// handleGetBuild devolve o estado atual do build (poll). 404 se não existe.
func (s *Server) handleGetBuild(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, err := s.bd.Get(r.Context(), id)
	if err != nil {
		s.fail(w, "build-get", err)
		return
	}
	if st == nil {
		writeError(w, http.StatusNotFound, "build não encontrado")
		return
	}
	writeJSON(w, http.StatusOK, toBuildResponse(st))
}

// handleDeleteBuild remove o container de build (idempotente). O control plane chama
// após consumir o resultado terminal.
func (s *Server) handleDeleteBuild(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.bd.Delete(r.Context(), id); err != nil {
		s.fail(w, "build-delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// buildSpecFromRequest valida o corpo de build e monta o Spec; devolve uma mensagem
// de erro (string vazia = ok) que a camada HTTP transforma em 400. Todo campo que
// flui para args do Docker ou para o entrypoint passa por allowlist estrita.
func buildSpecFromRequest(req createBuildRequest) (builder.Spec, string) {
	if !reIdempotencyKey.MatchString(req.IdempotencyKey) {
		return builder.Spec{}, "idempotencyKey ausente ou em formato inválido"
	}
	if !reSlug.MatchString(req.Name) {
		return builder.Spec{}, "name em formato inválido"
	}
	if !reCloneURL.MatchString(req.RepoCloneURL) {
		return builder.Spec{}, "repoCloneUrl ausente ou em formato inválido"
	}
	if req.GitToken != "" && !reGitToken.MatchString(req.GitToken) {
		return builder.Spec{}, "gitToken em formato inválido"
	}
	if !reGitRef.MatchString(req.Ref) {
		return builder.Spec{}, "ref ausente ou em formato inválido"
	}
	if req.CommitSha != "" && !reCommitSha.MatchString(req.CommitSha) {
		return builder.Spec{}, "commitSha em formato inválido"
	}
	if !reImageRef.MatchString(req.ImageTarget) {
		return builder.Spec{}, "imageTarget ausente ou em formato inválido"
	}
	if req.Dockerfile != "" && !reDockerfilePath.MatchString(req.Dockerfile) {
		return builder.Spec{}, "dockerfile em formato inválido"
	}

	spec := builder.Spec{
		ID:             newBuildID(),
		IdempotencyKey: req.IdempotencyKey,
		Name:           req.Name,
		RepoCloneURL:   req.RepoCloneURL,
		GitToken:       req.GitToken,
		Ref:            req.Ref,
		CommitSha:      req.CommitSha,
		ImageTarget:    req.ImageTarget,
		Dockerfile:     req.Dockerfile,
		RegistryAuth: builder.RegistryAuth{
			Server:   req.Registry.Server,
			Username: req.Registry.Username,
			Password: req.Registry.Password,
		},
	}
	if req.Limits != nil {
		spec.MemoryMb = req.Limits.MemoryMb
		spec.CPUs = req.Limits.CPUs
	}
	return spec, ""
}

// toBuildResponse mapeia o estado do builder para o DTO.
func toBuildResponse(st *builder.Status) buildResponse {
	return buildResponse{
		ID:       st.ID,
		Status:   st.Status,
		ImageRef: st.ImageRef,
		Digest:   st.Digest,
		Logs:     st.Logs,
	}
}

// toResponse mapeia a instância do runtime para o DTO, montando a connection URL.
func (s *Server) toResponse(ctx context.Context, inst *runtime.Instance) resourceResponse {
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
		case runtime.KindApp:
			resp.Connection = &resourceConnection{
				URL:  s.buildAppURL(inst),
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
	if customs := s.customDomains(ctx, inst); len(customs) > 0 {
		resp.Metadata["customDomains"] = customs
	}
	return resp
}

// customDomains devolve os domínios próprios atualmente roteados para o app (os
// domínios do fragmento menos o subdomínio padrão). Best-effort: erro de leitura do
// fragmento vira lista vazia — não falha a resposta inteira por causa da metadata.
func (s *Server) customDomains(ctx context.Context, inst *runtime.Instance) []string {
	if inst.Kind != runtime.KindApp {
		return nil
	}
	base := s.appDomain(inst)
	if base == "" {
		return nil
	}
	all, err := s.router.Domains(ctx, inst.ID)
	if err != nil {
		// Best-effort: só loga (a metadata é opcional, a resposta principal segue).
		s.log.Warn("ler domínios do fragmento falhou", "id", inst.ID, "err", err)
		return nil
	}
	if len(all) == 0 {
		return nil
	}
	customs := make([]string, 0, len(all))
	for _, d := range all {
		if d != base {
			customs = append(customs, d)
		}
	}
	if len(customs) == 0 {
		return nil
	}
	return customs
}

// toMetricsResponse mapeia a amostra do runtime para o DTO. CollectedAt vira RFC3339
// (UTC) para o control plane parsear sem ambiguidade de timezone.
func toMetricsResponse(m *runtime.Metrics) metricsResponse {
	return metricsResponse{
		ID:               m.ID,
		Kind:             string(m.Kind),
		Status:           m.Status,
		CollectedAt:      m.CollectedAt.UTC().Format(time.RFC3339),
		CPUPercent:       m.CPUPercent,
		MemoryBytes:      m.MemoryBytes,
		MemoryLimitBytes: m.MemoryLimitBytes,
		NetRxBytes:       m.NetRxBytes,
		NetTxBytes:       m.NetTxBytes,
		DiskBytes:        m.DiskBytes,
		Keys:             m.Keys,
		UptimeSeconds:    m.UptimeSeconds,
	}
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

// appDomain devolve o domínio público do app (<id>.<AppsBaseDomain>) quando o
// roteamento está ligado, ou "" quando desligado. Usa o ID — estável e único por
// construção — como subdomínio; o Name não é garantidamente único, então evitá-lo
// no roteamento impede colisão de rotas entre apps homônimos.
func (s *Server) appDomain(inst *runtime.Instance) string {
	if s.cfg.AppsBaseDomain == "" {
		return ""
	}
	sub := proxy.Subdomain(inst.ID)
	if sub == "" {
		return ""
	}
	return sub + "." + s.cfg.AppsBaseDomain
}

// buildAppURL monta a URL pública do app. Com roteamento ligado, devolve
// https://<domínio> (o Caddy do host termina o TLS); sem roteamento, cai no
// http://host:port loopback (comportamento legado). App não tem credencial embutida.
func (s *Server) buildAppURL(inst *runtime.Instance) string {
	if domain := s.appDomain(inst); domain != "" {
		u := url.URL{Scheme: "https", Host: domain}
		return u.String()
	}
	u := url.URL{
		Scheme: "http",
		Host:   s.cfg.AdvertiseHost + ":" + strconv.Itoa(inst.HostPort),
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
	switch kind {
	case runtime.KindRedis:
		return "redis-" + suffix
	case runtime.KindApp:
		return "app-" + suffix
	default:
		return "pg-" + suffix
	}
}

// newBuildID gera um id único para o build. O prefixo "build-" identifica o kind no
// nome do container de build (adila-build-<sufixo>) e em logs.
func newBuildID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand falhou: %v", err))
	}
	return "build-" + hex.EncodeToString(b)
}
