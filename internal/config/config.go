// Package config carrega a configuração do agent a partir de variáveis de ambiente.
// Falha rápido (na inicialização) se um segredo obrigatório estiver ausente —
// melhor não subir do que subir sem autenticação.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config é a configuração resolvida do agent.
type Config struct {
	// Addr é o endereço de escuta do HTTP (default ":8080").
	Addr string
	// Token é o bearer token que autentica o control plane. OBRIGATÓRIO.
	Token string
	// AdvertiseHost é o host que entra na connection URL devolvida ao control plane.
	// Local: 127.0.0.1. Produção: o IP da rede privada (WireGuard) da box.
	AdvertiseHost string
	// BindHost é o IP onde a porta do container é publicada no host.
	BindHost string
	// SSLMode entra na connection URL (?sslmode=). Local: "disable". Prod: "require".
	SSLMode string
	// DefaultPgVersion é a tag usada quando o request não especifica versão para Postgres.
	DefaultPgVersion string
	// DefaultRedisVersion é a tag usada quando o request não especifica versão para Redis.
	DefaultRedisVersion string
	// DockerBin é o caminho do binário do Docker.
	DockerBin string
	// BuilderImage é a imagem do container de build (git + nixpacks + kaniko) usada
	// em POST /v1/builds. Default "adila-builder:latest".
	BuilderImage string
	// PortRangeStart/PortRangeEnd: range [start,end] de portas para Postgres (default 20000-29999).
	// Porta FIXA por tenant gravada em label — estável através de stop/start.
	PortRangeStart int
	PortRangeEnd   int
	// RedisPortRangeStart/RedisPortRangeEnd: range separado para Redis (default 30000-39999).
	// Range separado evita colisão entre Postgres e Redis no mesmo host.
	RedisPortRangeStart int
	RedisPortRangeEnd   int
	// AppPortRangeStart/AppPortRangeEnd: range separado para apps (default 40000-49999).
	AppPortRangeStart int
	AppPortRangeEnd   int
	// IdleStopAfter: containers rodando há mais que este tempo são parados automaticamente
	// (lever de custo: container parado consome ~0 RAM). Zero desabilita (default).
	IdleStopAfter time.Duration
	// BackupInterval: intervalo entre backups automáticos para R2. Zero desabilita (default).
	BackupInterval time.Duration
	// BackupRetention: quantos backups manter por recurso (default 7).
	BackupRetention int
	// R2*: credenciais do Cloudflare R2 para upload de backups.
	// Todos os quatro campos são obrigatórios quando BackupInterval > 0.
	R2AccountID       string
	R2Bucket          string
	R2AccessKeyID     string
	R2SecretAccessKey string
	// AppsBaseDomain liga o roteamento público dos apps. Vazio (default) = desligado:
	// apps publicam só em loopback, como hoje. Preenchido (ex.: "apps.adila.co") faz o
	// agent registrar <id>.<AppsBaseDomain> no Caddy do host a cada deploy.
	AppsBaseDomain string
	// CaddyAppsDir é o diretório dos fragmentos de rota (importado pelo Caddyfile
	// principal via `import <dir>/*.caddy`). Default "/etc/caddy/apps".
	CaddyAppsDir string
	// CaddyfilePath é o Caddyfile principal recarregado após cada mudança de rota.
	// Default "/etc/caddy/Caddyfile".
	CaddyfilePath string
	// CaddyBin é o binário do Caddy usado no reload. Default "caddy".
	CaddyBin string
}

// Load lê o ambiente e valida. Erro se Token estiver ausente, ranges de portas
// forem inválidos, ou BackupInterval > 0 sem as credenciais R2 completas.
func Load() (Config, error) {
	portStart, err := envInt("AGENT_PORT_RANGE_START", 20000)
	if err != nil {
		return Config{}, err
	}
	portEnd, err := envInt("AGENT_PORT_RANGE_END", 29999)
	if err != nil {
		return Config{}, err
	}
	redisPortStart, err := envInt("AGENT_REDIS_PORT_RANGE_START", 30000)
	if err != nil {
		return Config{}, err
	}
	redisPortEnd, err := envInt("AGENT_REDIS_PORT_RANGE_END", 39999)
	if err != nil {
		return Config{}, err
	}
	appPortStart, err := envInt("AGENT_APP_PORT_RANGE_START", 40000)
	if err != nil {
		return Config{}, err
	}
	appPortEnd, err := envInt("AGENT_APP_PORT_RANGE_END", 49999)
	if err != nil {
		return Config{}, err
	}
	idleStop, err := envDuration("AGENT_IDLE_STOP_AFTER", 0)
	if err != nil {
		return Config{}, err
	}
	backupInterval, err := envDuration("AGENT_BACKUP_INTERVAL", 0)
	if err != nil {
		return Config{}, err
	}
	backupRetention, err := envInt("AGENT_BACKUP_RETENTION", 7)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Addr:                env("AGENT_ADDR", ":8080"),
		Token:               strings.TrimSpace(os.Getenv("AGENT_TOKEN")),
		AdvertiseHost:       env("AGENT_ADVERTISE_HOST", "127.0.0.1"),
		BindHost:            env("AGENT_BIND_HOST", "127.0.0.1"),
		SSLMode:             env("AGENT_SSLMODE", "disable"),
		DefaultPgVersion:    env("AGENT_DEFAULT_PG_VERSION", "16"),
		DefaultRedisVersion: env("AGENT_DEFAULT_REDIS_VERSION", "7"),
		DockerBin:           env("AGENT_DOCKER_BIN", "docker"),
		BuilderImage:        env("AGENT_BUILDER_IMAGE", "adila-builder:latest"),
		PortRangeStart:      portStart,
		PortRangeEnd:        portEnd,
		RedisPortRangeStart: redisPortStart,
		RedisPortRangeEnd:   redisPortEnd,
		AppPortRangeStart:   appPortStart,
		AppPortRangeEnd:     appPortEnd,
		IdleStopAfter:       idleStop,
		BackupInterval:      backupInterval,
		BackupRetention:     backupRetention,
		R2AccountID:         os.Getenv("AGENT_R2_ACCOUNT_ID"),
		R2Bucket:            os.Getenv("AGENT_R2_BUCKET"),
		R2AccessKeyID:       os.Getenv("AGENT_R2_ACCESS_KEY_ID"),
		R2SecretAccessKey:   os.Getenv("AGENT_R2_SECRET_ACCESS_KEY"),
		AppsBaseDomain:      strings.ToLower(env("AGENT_APPS_BASE_DOMAIN", "")),
		CaddyAppsDir:        env("AGENT_CADDY_APPS_DIR", "/etc/caddy/apps"),
		CaddyfilePath:       env("AGENT_CADDYFILE", "/etc/caddy/Caddyfile"),
		CaddyBin:            env("AGENT_CADDY_BIN", "caddy"),
	}

	if cfg.Token == "" {
		return Config{}, fmt.Errorf("AGENT_TOKEN é obrigatório (bearer token do control plane)")
	}
	if cfg.PortRangeStart < 1 || cfg.PortRangeEnd > 65535 || cfg.PortRangeStart > cfg.PortRangeEnd {
		return Config{}, fmt.Errorf(
			"range de portas Postgres inválido: AGENT_PORT_RANGE_START=%d AGENT_PORT_RANGE_END=%d (precisa 1 ≤ start ≤ end ≤ 65535)",
			cfg.PortRangeStart, cfg.PortRangeEnd)
	}
	if cfg.RedisPortRangeStart < 1 || cfg.RedisPortRangeEnd > 65535 || cfg.RedisPortRangeStart > cfg.RedisPortRangeEnd {
		return Config{}, fmt.Errorf(
			"range de portas Redis inválido: AGENT_REDIS_PORT_RANGE_START=%d AGENT_REDIS_PORT_RANGE_END=%d (precisa 1 ≤ start ≤ end ≤ 65535)",
			cfg.RedisPortRangeStart, cfg.RedisPortRangeEnd)
	}
	if cfg.AppPortRangeStart < 1 || cfg.AppPortRangeEnd > 65535 || cfg.AppPortRangeStart > cfg.AppPortRangeEnd {
		return Config{}, fmt.Errorf(
			"range de portas App inválido: AGENT_APP_PORT_RANGE_START=%d AGENT_APP_PORT_RANGE_END=%d (precisa 1 ≤ start ≤ end ≤ 65535)",
			cfg.AppPortRangeStart, cfg.AppPortRangeEnd)
	}
	if cfg.BackupInterval > 0 && !cfg.R2Enabled() {
		return Config{}, fmt.Errorf(
			"AGENT_BACKUP_INTERVAL requer AGENT_R2_ACCOUNT_ID, AGENT_R2_BUCKET, AGENT_R2_ACCESS_KEY_ID e AGENT_R2_SECRET_ACCESS_KEY")
	}
	if cfg.AppsBaseDomain != "" && !reDomain.MatchString(cfg.AppsBaseDomain) {
		return Config{}, fmt.Errorf(
			"AGENT_APPS_BASE_DOMAIN inválido: %q (esperado um domínio como 'apps.adila.co')", cfg.AppsBaseDomain)
	}

	return cfg, nil
}

// reDomain valida AGENT_APPS_BASE_DOMAIN: rótulos DNS minúsculos separados por ponto,
// com pelo menos dois rótulos. Barra injeção no Caddyfile, já que o domínio compõe
// um bloco de config (<domínio> { ... }).
var reDomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// RoutingEnabled reporta se o roteamento público de apps está ligado.
func (c *Config) RoutingEnabled() bool {
	return c.AppsBaseDomain != ""
}

// R2Enabled reporta se as credenciais R2 estão completas (todos os quatro campos).
func (c *Config) R2Enabled() bool {
	return c.R2AccountID != "" && c.R2Bucket != "" &&
		c.R2AccessKeyID != "" && c.R2SecretAccessKey != ""
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// envInt lê uma variável inteira do ambiente, caindo no default se ausente e
// falhando explícito se presente mas não-numérica.
func envInt(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s deve ser um inteiro: %w", key, err)
	}
	return n, nil
}

// envDuration lê uma variável de duração (ex.: "1h", "30m"), caindo no default
// se ausente e falhando explícito se presente mas inválida.
func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s deve ser uma duração válida (ex.: '1h', '30m'): %w", key, err)
	}
	return d, nil
}
