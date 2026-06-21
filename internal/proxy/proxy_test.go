package proxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderBlock(t *testing.T) {
	got := RenderBlock([]string{"app-abc.apps.adila.co"}, "127.0.0.1", 40123)
	want := "app-abc.apps.adila.co {\n\treverse_proxy 127.0.0.1:40123\n}\n"
	if got != want {
		t.Fatalf("bloco gerado divergente:\n got: %q\nwant: %q", got, want)
	}
}

func TestRenderBlockMultiplosDominios(t *testing.T) {
	got := RenderBlock([]string{"app-abc.apps.adila.co", "lp.adila.co", "www.lp.adila.co"}, "127.0.0.1", 40123)
	want := "app-abc.apps.adila.co, lp.adila.co, www.lp.adila.co {\n\treverse_proxy 127.0.0.1:40123\n}\n"
	if got != want {
		t.Fatalf("bloco multi-domínio divergente:\n got: %q\nwant: %q", got, want)
	}
}

func TestParseDomainsInverteRenderBlock(t *testing.T) {
	domains := []string{"app-abc.apps.adila.co", "lp.adila.co", "www.lp.adila.co"}
	block := RenderBlock(domains, "127.0.0.1", 40123)
	got := ParseDomains(block)
	if len(got) != len(domains) {
		t.Fatalf("ParseDomains devolveu %d domínios, quer %d: %v", len(got), len(domains), got)
	}
	for i := range domains {
		if got[i] != domains[i] {
			t.Fatalf("domínio %d = %q, quer %q", i, got[i], domains[i])
		}
	}
	if ParseDomains("sem chave nenhuma") != nil {
		t.Fatal("bloco sem '{' deveria devolver nil")
	}
}

func TestSubdomain(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"id do agent passa intacto", "app-a1b2c3d4", "app-a1b2c3d4"},
		{"maiúsculas viram minúsculas", "App-XYZ", "app-xyz"},
		{"caracteres inválidos colapsam em hífen", "my app/v2", "my-app-v2"},
		{"hífens nas pontas são removidos", "__abc__", "abc"},
		{"só inválidos resulta em vazio", "***", ""},
		{"trunca em 63 chars", strings.Repeat("a", 80), strings.Repeat("a", 63)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Subdomain(c.in); got != c.want {
				t.Fatalf("Subdomain(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNoopRouter(t *testing.T) {
	var r Router = NoopRouter{}
	if err := r.Upsert(context.Background(), "app-x", []string{"x.apps.adila.co"}, 8080); err != nil {
		t.Fatalf("Upsert noop: %v", err)
	}
	if err := r.Remove(context.Background(), "app-x"); err != nil {
		t.Fatalf("Remove noop: %v", err)
	}
}

// newTestRouter monta um CaddyRouter apontando para um diretório temporário e com
// um reload falso que apenas conta as chamadas — sem invocar o binário do Caddy.
func newTestRouter(t *testing.T) (*CaddyRouter, *int) {
	t.Helper()
	dir := t.TempDir()
	reloads := 0
	r := &CaddyRouter{
		appsDir:  filepath.Join(dir, "apps"),
		bindHost: "127.0.0.1",
		reload: func(context.Context) error {
			reloads++
			return nil
		},
	}
	return r, &reloads
}

func TestCaddyRouterUpsertWritesFragmentAndReloads(t *testing.T) {
	r, reloads := newTestRouter(t)

	if err := r.Upsert(context.Background(), "app-abc", []string{"app-abc.apps.adila.co"}, 40123); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	path := filepath.Join(r.appsDir, "app-abc.caddy")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ler fragmento: %v", err)
	}
	want := RenderBlock([]string{"app-abc.apps.adila.co"}, "127.0.0.1", 40123)
	if string(data) != want {
		t.Fatalf("conteúdo do fragmento divergente:\n got: %q\nwant: %q", data, want)
	}
	if *reloads != 1 {
		t.Fatalf("esperava 1 reload, obteve %d", *reloads)
	}

	// Nenhum arquivo temporário (.tmp) deve sobrar após a escrita atômica.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("arquivo .tmp não foi removido após rename")
	}
}

func TestCaddyRouterUpsertIsIdempotent(t *testing.T) {
	r, reloads := newTestRouter(t)
	ctx := context.Background()

	if err := r.Upsert(ctx, "app-abc", []string{"app-abc.apps.adila.co"}, 1111); err != nil {
		t.Fatalf("primeiro Upsert: %v", err)
	}
	// Reescrever a mesma rota (porta nova) deve sobrescrever sem erro.
	if err := r.Upsert(ctx, "app-abc", []string{"app-abc.apps.adila.co"}, 2222); err != nil {
		t.Fatalf("segundo Upsert: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(r.appsDir, "app-abc.caddy"))
	if err != nil {
		t.Fatalf("ler fragmento: %v", err)
	}
	if !strings.Contains(string(data), "127.0.0.1:2222") {
		t.Fatalf("fragmento não refletiu a porta atualizada: %q", data)
	}
	if *reloads != 2 {
		t.Fatalf("esperava 2 reloads, obteve %d", *reloads)
	}
}

func TestCaddyRouterDomainsLeFragmento(t *testing.T) {
	r, _ := newTestRouter(t)
	ctx := context.Background()

	// Sem fragmento, a lista é vazia (não é erro).
	got, err := r.Domains(ctx, "app-abc")
	if err != nil {
		t.Fatalf("Domains sem fragmento: %v", err)
	}
	if got != nil {
		t.Fatalf("esperava nil sem fragmento, obteve %v", got)
	}

	domains := []string{"app-abc.apps.adila.co", "lp.adila.co"}
	if err := r.Upsert(ctx, "app-abc", domains, 40123); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err = r.Domains(ctx, "app-abc")
	if err != nil {
		t.Fatalf("Domains: %v", err)
	}
	if len(got) != 2 || got[0] != domains[0] || got[1] != domains[1] {
		t.Fatalf("Domains = %v, quer %v", got, domains)
	}
}

func TestCaddyRouterRemove(t *testing.T) {
	r, reloads := newTestRouter(t)
	ctx := context.Background()

	if err := r.Upsert(ctx, "app-abc", []string{"app-abc.apps.adila.co"}, 40123); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := r.Remove(ctx, "app-abc"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.appsDir, "app-abc.caddy")); !os.IsNotExist(err) {
		t.Fatalf("fragmento não foi removido")
	}
	// 1 reload do Upsert + 1 do Remove.
	if *reloads != 2 {
		t.Fatalf("esperava 2 reloads, obteve %d", *reloads)
	}
}

func TestCaddyRouterRemoveIsIdempotent(t *testing.T) {
	r, reloads := newTestRouter(t)
	// Remover rota inexistente é no-op e NÃO recarrega o Caddy (nada mudou).
	if err := r.Remove(context.Background(), "app-inexistente"); err != nil {
		t.Fatalf("Remove idempotente deveria ser no-op, obteve: %v", err)
	}
	if *reloads != 0 {
		t.Fatalf("Remove de rota ausente não deveria recarregar; reloads=%d", *reloads)
	}
}

func TestCaddyRouterUpsertPropagatesReloadError(t *testing.T) {
	r, _ := newTestRouter(t)
	sentinel := errors.New("reload explodiu")
	r.reload = func(context.Context) error { return sentinel }

	err := r.Upsert(context.Background(), "app-abc", []string{"app-abc.apps.adila.co"}, 40123)
	if !errors.Is(err, sentinel) {
		t.Fatalf("erro do reload deveria propagar; obteve: %v", err)
	}
	// O fragmento já foi escrito antes do reload falhar (será reconciliado no retry).
	if _, err := os.Stat(filepath.Join(r.appsDir, "app-abc.caddy")); err != nil {
		t.Fatalf("fragmento deveria existir mesmo com reload falho: %v", err)
	}
}

// TestNewCaddyRouterReloadInvokesBinary exercita o reload real construído por
// NewCaddyRouter: com um binário inexistente, o Upsert escreve o fragmento e então
// falha ao recarregar — cobrindo a closure de reload e a formatação do erro.
func TestNewCaddyRouterReloadInvokesBinary(t *testing.T) {
	dir := t.TempDir()
	r := NewCaddyRouter(filepath.Join(dir, "apps"), "127.0.0.1", "caddy-que-nao-existe-xyz", filepath.Join(dir, "Caddyfile"))

	err := r.Upsert(context.Background(), "app-abc", []string{"app-abc.apps.adila.co"}, 40123)
	if err == nil {
		t.Fatal("esperava erro do reload com binário inexistente")
	}
	if !strings.Contains(err.Error(), "caddy reload falhou") {
		t.Fatalf("mensagem de erro inesperada: %v", err)
	}
	// Mesmo com reload falho, o fragmento foi materializado.
	if _, statErr := os.Stat(filepath.Join(dir, "apps", "app-abc.caddy")); statErr != nil {
		t.Fatalf("fragmento deveria existir: %v", statErr)
	}
}

func TestCaddyRouterRejectsUnsafeID(t *testing.T) {
	r, reloads := newTestRouter(t)
	ctx := context.Background()

	bad := []string{"../../etc/caddy/Caddyfile", "a/b", "with space", ""}
	for _, id := range bad {
		if err := r.Upsert(ctx, id, []string{"x.apps.adila.co"}, 8080); err == nil {
			t.Fatalf("Upsert deveria rejeitar id inseguro %q", id)
		}
		if err := r.Remove(ctx, id); err == nil {
			t.Fatalf("Remove deveria rejeitar id inseguro %q", id)
		}
	}
	if *reloads != 0 {
		t.Fatalf("ids inseguros não deveriam recarregar; reloads=%d", *reloads)
	}
}
