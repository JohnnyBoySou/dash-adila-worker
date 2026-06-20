package config

import "testing"

func TestLoadDefaultsPortRange(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "tok")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PortRangeStart != 20000 || cfg.PortRangeEnd != 29999 {
		t.Fatalf("range default = %d-%d, quer 20000-29999", cfg.PortRangeStart, cfg.PortRangeEnd)
	}
}

func TestLoadRequiresToken(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "") // explicitamente vazio → deve falhar
	if _, err := Load(); err == nil {
		t.Fatal("sem token: quer erro, veio nil")
	}
}

func TestLoadRejectsNonNumericPort(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "tok")
	t.Setenv("AGENT_PORT_RANGE_START", "abc")
	if _, err := Load(); err == nil {
		t.Fatal("porta não-numérica: quer erro, veio nil")
	}
}

func TestRoutingDisabledByDefault(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "tok")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RoutingEnabled() {
		t.Fatal("roteamento deveria vir desligado sem AGENT_APPS_BASE_DOMAIN")
	}
	// Defaults dos caminhos do Caddy devem estar populados mesmo com roteamento off.
	if cfg.CaddyAppsDir != "/etc/caddy/apps" || cfg.CaddyfilePath != "/etc/caddy/Caddyfile" || cfg.CaddyBin != "caddy" {
		t.Fatalf("defaults do Caddy inesperados: %+v", cfg)
	}
}

func TestRoutingEnabledWithValidDomain(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "tok")
	t.Setenv("AGENT_APPS_BASE_DOMAIN", "Apps.Adila.Co") // maiúsculas devem normalizar
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.RoutingEnabled() {
		t.Fatal("roteamento deveria estar ligado com AGENT_APPS_BASE_DOMAIN")
	}
	if cfg.AppsBaseDomain != "apps.adila.co" {
		t.Fatalf("AppsBaseDomain = %q, quer minúsculo 'apps.adila.co'", cfg.AppsBaseDomain)
	}
}

func TestLoadRejectsInvalidAppsBaseDomain(t *testing.T) {
	for _, bad := range []string{"semponto", "-x.adila.co", "x..adila.co", "x.adila.co/path", "a b.adila.co"} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("AGENT_TOKEN", "tok")
			t.Setenv("AGENT_APPS_BASE_DOMAIN", bad)
			if _, err := Load(); err == nil {
				t.Fatalf("domínio inválido %q: quer erro, veio nil", bad)
			}
		})
	}
}

func TestLoadAcceptsCustomCaddyPaths(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "tok")
	t.Setenv("AGENT_CADDY_APPS_DIR", "/srv/caddy/apps")
	t.Setenv("AGENT_CADDYFILE", "/srv/caddy/Caddyfile")
	t.Setenv("AGENT_CADDY_BIN", "/usr/local/bin/caddy")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CaddyAppsDir != "/srv/caddy/apps" || cfg.CaddyfilePath != "/srv/caddy/Caddyfile" || cfg.CaddyBin != "/usr/local/bin/caddy" {
		t.Fatalf("overrides do Caddy não aplicados: %+v", cfg)
	}
}

func TestLoadRejectsInvalidPortRange(t *testing.T) {
	cases := []struct {
		name       string
		start, end string
	}{
		{"start maior que end", "30000", "20000"},
		{"start abaixo de 1", "0", "29999"},
		{"end acima de 65535", "20000", "70000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENT_TOKEN", "tok")
			t.Setenv("AGENT_PORT_RANGE_START", tc.start)
			t.Setenv("AGENT_PORT_RANGE_END", tc.end)
			if _, err := Load(); err == nil {
				t.Fatalf("%s: quer erro, veio nil", tc.name)
			}
		})
	}
}
