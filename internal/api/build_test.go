package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Corpo de build válido reaproveitado entre os testes.
const validBuildBody = `{"idempotencyKey":"b1","name":"web-abc","repoCloneUrl":"https://github.com/owner/repo.git","gitToken":"ghs_abc123","ref":"main","commitSha":"a1b2c3d","imageTarget":"registry.interno:5000/org/web:a1b2c3d","registry":{"server":"registry.interno:5000","username":"u","password":"p"}}`

func decodeBuild(t *testing.T, resp *http.Response) buildResponse {
	t.Helper()
	defer resp.Body.Close()
	var out buildResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decodificar corpo: %v", err)
	}
	return out
}

func TestBuildRequiresAuth(t *testing.T) {
	ts, _, _ := newTestServerWithBuilder()
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/builds", "", validBuildBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sem token: status = %d, quer 401", resp.StatusCode)
	}
}

func TestBuildStartsAsync(t *testing.T) {
	ts, _, _ := newTestServerWithBuilder()
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/builds", testToken, validBuildBody)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, quer 202", resp.StatusCode)
	}
	b := decodeBuild(t, resp)
	if !strings.HasPrefix(b.ID, "build-") {
		t.Fatalf("id inesperado: %q (quer prefixo build-)", b.ID)
	}
	if b.Status != "running" {
		t.Fatalf("status = %q, quer running", b.Status)
	}
	// Build assíncrono: a imagem ainda não existe na resposta inicial.
	if b.ImageRef != "" {
		t.Fatalf("imageRef deveria estar vazio enquanto running, veio %q", b.ImageRef)
	}
}

func TestBuildPollUntilSucceeded(t *testing.T) {
	ts, _, bd := newTestServerWithBuilder()
	defer ts.Close()

	created := decodeBuild(t, do(t, ts, "POST", "/v1/builds", testToken, validBuildBody))

	// Poll inicial: ainda running.
	running := decodeBuild(t, do(t, ts, "GET", "/v1/builds/"+created.ID, testToken, ""))
	if running.Status != "running" {
		t.Fatalf("status = %q, quer running", running.Status)
	}

	// Simula a conclusão do build (o que o container kaniko faria).
	bd.Complete(created.ID, "sha256:"+strings.Repeat("a", 64))

	done := decodeBuild(t, do(t, ts, "GET", "/v1/builds/"+created.ID, testToken, ""))
	if done.Status != "succeeded" {
		t.Fatalf("status = %q, quer succeeded", done.Status)
	}
	if done.ImageRef != "registry.interno:5000/org/web:a1b2c3d" {
		t.Fatalf("imageRef = %q", done.ImageRef)
	}
	if !strings.HasPrefix(done.Digest, "sha256:") {
		t.Fatalf("digest = %q, quer sha256:...", done.Digest)
	}
}

func TestBuildPollUntilFailed(t *testing.T) {
	ts, _, bd := newTestServerWithBuilder()
	defer ts.Close()

	created := decodeBuild(t, do(t, ts, "POST", "/v1/builds", testToken, validBuildBody))
	bd.Fail(created.ID, "ERROR: npm install falhou")

	failed := decodeBuild(t, do(t, ts, "GET", "/v1/builds/"+created.ID, testToken, ""))
	if failed.Status != "failed" {
		t.Fatalf("status = %q, quer failed", failed.Status)
	}
	if failed.ImageRef != "" {
		t.Fatalf("build falho não deveria ter imageRef, veio %q", failed.ImageRef)
	}
	if !strings.Contains(failed.Logs, "npm install falhou") {
		t.Fatalf("logs não propagados: %q", failed.Logs)
	}
}

func TestBuildGetMissingReturns404(t *testing.T) {
	ts, _, _ := newTestServerWithBuilder()
	defer ts.Close()

	resp := do(t, ts, "GET", "/v1/builds/build-nao-existe", testToken, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, quer 404", resp.StatusCode)
	}
}

func TestBuildDeleteIsIdempotent(t *testing.T) {
	ts, _, _ := newTestServerWithBuilder()
	defer ts.Close()

	created := decodeBuild(t, do(t, ts, "POST", "/v1/builds", testToken, validBuildBody))

	first := do(t, ts, "DELETE", "/v1/builds/"+created.ID, testToken, "")
	first.Body.Close()
	if first.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, quer 204", first.StatusCode)
	}
	// Após delete, GET dá 404.
	get := do(t, ts, "GET", "/v1/builds/"+created.ID, testToken, "")
	get.Body.Close()
	if get.StatusCode != http.StatusNotFound {
		t.Fatalf("get pós-delete: status = %d, quer 404", get.StatusCode)
	}
	// Segunda remoção (já removido) ainda é 204 — idempotente.
	second := do(t, ts, "DELETE", "/v1/builds/"+created.ID, testToken, "")
	second.Body.Close()
	if second.StatusCode != http.StatusNoContent {
		t.Fatalf("delete repetido: status = %d, quer 204", second.StatusCode)
	}
}

// Validação de borda: campos de build que fluem para args do Docker / entrypoint
// precisam de allowlist estrita.
func TestBuildRejectsMalformedInput(t *testing.T) {
	ts, _, _ := newTestServerWithBuilder()
	defer ts.Close()

	cases := []struct {
		name string
		body string
	}{
		{"idempotencyKey ausente", `{"name":"web","repoCloneUrl":"https://github.com/o/r.git","ref":"main","imageTarget":"reg/o/r:t"}`},
		{"repoCloneUrl sem https", `{"idempotencyKey":"b1","repoCloneUrl":"http://github.com/o/r.git","ref":"main","imageTarget":"reg/o/r:t"}`},
		{"repoCloneUrl sem .git", `{"idempotencyKey":"b1","repoCloneUrl":"https://github.com/o/r","ref":"main","imageTarget":"reg/o/r:t"}`},
		{"repoCloneUrl com token embutido", `{"idempotencyKey":"b1","repoCloneUrl":"https://x:y@github.com/o/r.git","ref":"main","imageTarget":"reg/o/r:t"}`},
		{"ref com espaço", `{"idempotencyKey":"b1","repoCloneUrl":"https://github.com/o/r.git","ref":"ma in","imageTarget":"reg/o/r:t"}`},
		{"gitToken com espaço", `{"idempotencyKey":"b1","repoCloneUrl":"https://github.com/o/r.git","gitToken":"gh s","ref":"main","imageTarget":"reg/o/r:t"}`},
		{"commitSha inválido", `{"idempotencyKey":"b1","repoCloneUrl":"https://github.com/o/r.git","ref":"main","commitSha":"zzz","imageTarget":"reg/o/r:t"}`},
		{"imageTarget com espaço", `{"idempotencyKey":"b1","repoCloneUrl":"https://github.com/o/r.git","ref":"main","imageTarget":"reg/o r:t"}`},
		{"dockerfile com espaço", `{"idempotencyKey":"b1","repoCloneUrl":"https://github.com/o/r.git","ref":"main","imageTarget":"reg/o/r:t","dockerfile":"sub dir/Dockerfile"}`},
		{"imageTarget ausente", `{"idempotencyKey":"b1","repoCloneUrl":"https://github.com/o/r.git","ref":"main"}`},
		{"ref ausente", `{"idempotencyKey":"b1","repoCloneUrl":"https://github.com/o/r.git","imageTarget":"reg/o/r:t"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, ts, "POST", "/v1/builds", testToken, tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, quer 400", resp.StatusCode)
			}
		})
	}
}
