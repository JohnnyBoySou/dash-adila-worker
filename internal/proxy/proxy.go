// Package proxy registra rotas de reverse-proxy no Caddy do host para os apps
// deployados. Cada app vira um fragmento de Caddyfile (<id>.caddy) num diretório
// importado pelo Caddyfile principal (import .../apps/*.caddy); um `caddy reload`
// aplica a mudança sem downtime e o TLS é emitido automaticamente pelo Caddy.
//
// Por que fragmentos + reload em vez da Admin API: na box o Caddy roda via systemd
// a partir do Caddyfile, então config injetada pela Admin API NÃO sobrevive a um
// restart (o Caddy recarrega do Caddyfile). Os fragmentos são a fonte de verdade
// persistente — sobrevivem a restart do Caddy e do agent.
package proxy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Router registra/remove a rota pública de um app.
type Router interface {
	// Upsert cria ou atualiza a rota domains -> <bindHost>:port para o app id. Um
	// app pode responder em vários domínios (o subdomínio padrão + domínios custom);
	// todos compartilham o mesmo upstream num único bloco. Idempotente: reescrever a
	// mesma rota é seguro.
	Upsert(ctx context.Context, id string, domains []string, upstreamPort int) error
	// Remove apaga a rota do app id. Idempotente: rota inexistente é no-op.
	Remove(ctx context.Context, id string) error
	// Domains devolve os domínios atualmente roteados para o app id (lidos do
	// fragmento, que é a fonte de verdade persistente). Lista vazia se não houver rota.
	Domains(ctx context.Context, id string) ([]string, error)
}

// NoopRouter é o Router usado quando o roteamento público está desligado
// (AppsBaseDomain vazio). Mantém o comportamento legado: o agent publica o
// container em loopback e não toca no Caddy.
type NoopRouter struct{}

func (NoopRouter) Upsert(context.Context, string, []string, int) error { return nil }
func (NoopRouter) Remove(context.Context, string) error                { return nil }
func (NoopRouter) Domains(context.Context, string) ([]string, error)   { return nil, nil }

// reSafeID restringe o id a um nome de arquivo seguro — barra path traversal
// (ex.: "../../etc/...") já que o id compõe o caminho do fragmento. O id real é
// gerado pelo agent (ex.: "app-a1b2c3d4"), então a allowlist é folgada mas estrita.
var reSafeID = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// CaddyRouter materializa as rotas como fragmentos de Caddyfile + `caddy reload`.
type CaddyRouter struct {
	appsDir  string // diretório dos fragmentos (importado pelo Caddyfile principal)
	bindHost string // host do upstream (mesmo BindHost onde o container publica)
	reload   func(ctx context.Context) error
}

// NewCaddyRouter monta um CaddyRouter. O reload usa o mesmo comando do ExecReload
// do systemd na box: `caddy reload --config <caddyfile> --force`.
func NewCaddyRouter(appsDir, bindHost, caddyBin, caddyfile string) *CaddyRouter {
	r := &CaddyRouter{appsDir: appsDir, bindHost: bindHost}
	r.reload = func(ctx context.Context) error {
		out, err := exec.CommandContext(ctx, caddyBin, "reload", "--config", caddyfile, "--force").CombinedOutput()
		if err != nil {
			return fmt.Errorf("caddy reload falhou: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return r
}

func (r *CaddyRouter) fragmentPath(id string) (string, error) {
	if !reSafeID.MatchString(id) {
		return "", fmt.Errorf("id inválido para nome de fragmento: %q", id)
	}
	// filepath.Base é defesa em profundidade contra traversal, além do regex.
	return filepath.Join(r.appsDir, filepath.Base(id)+".caddy"), nil
}

// Upsert escreve o fragmento de forma atômica (tmp + rename) e recarrega o Caddy.
func (r *CaddyRouter) Upsert(ctx context.Context, id string, domains []string, upstreamPort int) error {
	path, err := r.fragmentPath(id)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return fmt.Errorf("nenhum domínio informado para a rota %q", id)
	}
	if err := os.MkdirAll(r.appsDir, 0o755); err != nil {
		return fmt.Errorf("criar diretório de rotas: %w", err)
	}
	block := RenderBlock(domains, r.bindHost, upstreamPort)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(block), 0o644); err != nil {
		return fmt.Errorf("escrever fragmento: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("instalar fragmento: %w", err)
	}
	return r.reload(ctx)
}

// Domains lê o fragmento do app e devolve os domínios roteados (os endereços do
// bloco Caddy, na ordem). Lista vazia se o fragmento não existir — o fragmento é a
// fonte de verdade persistente, então isso sobrevive a restart do agent e do Caddy.
func (r *CaddyRouter) Domains(_ context.Context, id string) ([]string, error) {
	path, err := r.fragmentPath(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ler fragmento: %w", err)
	}
	return ParseDomains(string(data)), nil
}

// Remove apaga o fragmento do app e recarrega o Caddy. Idempotente.
func (r *CaddyRouter) Remove(ctx context.Context, id string) error {
	path, err := r.fragmentPath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("remover fragmento: %w", err)
	}
	return r.reload(ctx)
}

// RenderBlock gera o bloco Caddyfile de um app. Caddy aceita vários endereços de
// site separados por vírgula compartilhando a mesma config, então um app com
// subdomínio padrão + domínios custom vira um único bloco com um upstream. Puro e
// exportado para teste.
func RenderBlock(domains []string, upstreamHost string, port int) string {
	head := strings.Join(domains, ", ")
	return fmt.Sprintf("%s {\n\treverse_proxy %s:%d\n}\n", head, upstreamHost, port)
}

// ParseDomains extrai os endereços de site de um bloco gerado por RenderBlock: tudo
// antes do primeiro '{', separado por vírgula. Inverso de RenderBlock; tolera espaços.
func ParseDomains(block string) []string {
	i := strings.IndexByte(block, '{')
	if i < 0 {
		return nil
	}
	var out []string
	for _, part := range strings.Split(block[:i], ",") {
		if d := strings.TrimSpace(part); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// Subdomain converte um texto livre num label DNS válido (minúsculo, [a-z0-9-],
// sem hífen nas pontas, ≤63 chars). Retorna "" se nada sobrar de utilizável.
func Subdomain(raw string) string {
	var b strings.Builder
	prevDash := false
	for _, c := range strings.ToLower(raw) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
			prevDash = false
		default:
			// colapsa qualquer separador em um único '-' (e nunca no início).
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	return s
}
