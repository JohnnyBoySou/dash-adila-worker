package builder

import "testing"

func TestMapBuildState(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		exitCode int
		want     string
	}{
		{"rodando", "running", 0, StatusRunning},
		{"criado", "created", 0, StatusRunning},
		{"reiniciando", "restarting", 0, StatusRunning},
		{"pausado", "paused", 0, StatusRunning},
		{"exited zero é sucesso", "exited", 0, StatusSucceeded},
		{"exited não-zero é falha", "exited", 1, StatusFailed},
		{"exited 137 (OOM/kill) é falha", "exited", 137, StatusFailed},
		{"dead é falha terminal", "dead", 0, StatusFailed},
		{"desconhecido é falha", "qualquer", 0, StatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapBuildState(tc.status, tc.exitCode)
			if got != tc.want {
				t.Fatalf("mapBuildState(%q, %d) = %q, quer %q", tc.status, tc.exitCode, got, tc.want)
			}
		})
	}
}

func TestParseDigestFromLogs(t *testing.T) {
	const digest = "sha256:abc123def4567890abc123def4567890abc123def4567890abc123def4567890"
	logs := "passo 1...\npasso 2...\nADILA_IMAGE_DIGEST=" + digest + "\nfim do push\n"

	m := reDigest.FindStringSubmatch(logs)
	if m == nil {
		t.Fatal("digest não encontrado nos logs")
	}
	if m[1] != digest {
		t.Fatalf("digest = %q, quer %q", m[1], digest)
	}
}

func TestParseDigestWithTimestampPrefix(t *testing.T) {
	const digest = "sha256:abc123def4567890abc123def4567890abc123def4567890abc123def4567890"
	// Saída de `docker logs --timestamps`: cada linha vem com carimbo RFC3339Nano.
	logs := "2026-06-20T14:02:30.111Z passo 1...\n" +
		"2026-06-20T14:02:48.222Z ADILA_IMAGE_DIGEST=" + digest + "\n" +
		"2026-06-20T14:02:49.333Z fim do push\n"

	m := reDigest.FindStringSubmatch(logs)
	if m == nil {
		t.Fatal("digest não encontrado nos logs carimbados")
	}
	if m[1] != digest {
		t.Fatalf("digest = %q, quer %q", m[1], digest)
	}
}

func TestParseDigestAbsentReturnsNoMatch(t *testing.T) {
	logs := "build sem linha-sentinela de digest\n"
	if m := reDigest.FindStringSubmatch(logs); m != nil {
		t.Fatalf("não deveria casar digest, casou %v", m)
	}
}

func TestBuildContainerName(t *testing.T) {
	if got := buildContainerName("build-abc"); got != "adila-build-abc" {
		t.Fatalf("buildContainerName = %q, quer adila-build-abc", got)
	}
}

func TestIsNoSuchObject(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"Error: No such container: adila-build-x", true},
		{"Error: No such object: foo", true},
		{"some other docker error", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isNoSuchObject(errorString(tc.msg))
		if got != tc.want {
			t.Fatalf("isNoSuchObject(%q) = %v, quer %v", tc.msg, got, tc.want)
		}
	}
}

// errorString é um error trivial para os testes (msg vazia → nil-ish via guard).
type errorString string

func (e errorString) Error() string { return string(e) }
