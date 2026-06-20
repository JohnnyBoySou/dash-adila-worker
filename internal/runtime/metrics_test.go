package runtime

import "testing"

func TestParseDockerSize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int64
	}{
		{"bytes simples", "800B", 800},
		{"zero", "0B", 0},
		{"traço vira zero", "--", 0},
		{"vazio vira zero", "", 0},
		{"KiB binário", "1KiB", 1024},
		{"MiB binário", "25.5MiB", int64(25.5 * 1024 * 1024)},
		{"GiB binário", "4GiB", 4 * 1024 * 1024 * 1024},
		{"kB SI", "1.2kB", 1200},
		{"MB SI", "10MB", 10_000_000},
		{"GB SI", "2GB", 2_000_000_000},
		{"número puro vira bytes", "512", 512},
		{"unidade desconhecida vira zero", "5XB", 0},
		{"espaços ao redor", "  64MiB  ", 64 * 1024 * 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDockerSize(tc.in); got != tc.want {
				t.Fatalf("parseDockerSize(%q) = %d, quer %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestParsePercent(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"0.00%", 0},
		{"15.50%", 15.5},
		{"100%", 100},
		{"  3.2%  ", 3.2},
		{"lixo", 0},
	}
	for _, tc := range cases {
		if got := parsePercent(tc.in); got != tc.want {
			t.Fatalf("parsePercent(%q) = %v, quer %v", tc.in, got, tc.want)
		}
	}
}

func TestSplitSlash(t *testing.T) {
	a, b := splitSlash("25.5MiB / 4GiB")
	if a != "25.5MiB" || b != "4GiB" {
		t.Fatalf("splitSlash = (%q,%q), quer (25.5MiB,4GiB)", a, b)
	}
	a, b = splitSlash("semBarra")
	if a != "semBarra" || b != "" {
		t.Fatalf("sem barra: (%q,%q), quer (semBarra,\"\")", a, b)
	}
}

func TestParseStats(t *testing.T) {
	js := dockerStatsJSON{
		CPUPerc:  "12.50%",
		MemUsage: "100MiB / 2GiB",
		NetIO:    "1.5kB / 800B",
	}
	s := parseStats(js)
	if s.cpuPercent != 12.5 {
		t.Fatalf("cpu = %v, quer 12.5", s.cpuPercent)
	}
	if s.memBytes != 100*1024*1024 {
		t.Fatalf("mem = %d, quer %d", s.memBytes, 100*1024*1024)
	}
	if s.memLimitBytes != 2*1024*1024*1024 {
		t.Fatalf("memLimit = %d", s.memLimitBytes)
	}
	if s.netRxBytes != 1500 {
		t.Fatalf("netRx = %d, quer 1500", s.netRxBytes)
	}
	if s.netTxBytes != 800 {
		t.Fatalf("netTx = %d, quer 800", s.netTxBytes)
	}
}

func TestServerlessLabels(t *testing.T) {
	// Não-serverless: nenhum label.
	if got := serverlessLabels(Spec{Serverless: false, IdleTimeoutSeconds: 600}); got != nil {
		t.Fatalf("provisioned não devia ter labels, veio %v", got)
	}
	// Serverless com timeout explícito.
	got := serverlessLabels(Spec{Serverless: true, IdleTimeoutSeconds: 600})
	want := []string{"--label", "adila.serverless=true", "--label", "adila.idle.timeout=600"}
	if len(got) != len(want) {
		t.Fatalf("labels = %v, quer %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("labels[%d] = %q, quer %q", i, got[i], want[i])
		}
	}
	// Serverless sem timeout: só o flag (idle-stop usa o default do agent).
	got = serverlessLabels(Spec{Serverless: true})
	if len(got) != 2 || got[1] != "adila.serverless=true" {
		t.Fatalf("serverless sem timeout = %v, quer só o flag", got)
	}
}
