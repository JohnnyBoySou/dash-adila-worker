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
