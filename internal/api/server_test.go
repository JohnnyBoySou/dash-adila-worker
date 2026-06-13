package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adila/dash/worker/internal/runtime"
)

const testToken = "secret-token"

func newTestServer() (*httptest.Server, *runtime.Fake) {
	fake := runtime.NewFake()
	srv := NewServer(fake, Config{
		Token:               testToken,
		AdvertiseHost:       "10.0.0.5",
		SSLMode:             "require",
		DefaultPgVersion:    "16",
		DefaultRedisVersion: "7",
	}, nil)
	return httptest.NewServer(srv.Handler()), fake
}

func do(t *testing.T, ts *httptest.Server, method, path, token, body string) *http.Response {
	t.Helper()
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatalf("montar request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("executar request: %v", err)
	}
	return resp
}

func decodeResource(t *testing.T, resp *http.Response) resourceResponse {
	t.Helper()
	defer resp.Body.Close()
	var out resourceResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decodificar corpo: %v", err)
	}
	return out
}

func TestCreateRequiresAuth(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/resources", "", `{"idempotencyKey":"k1","name":"svc"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sem token: status = %d, quer 401", resp.StatusCode)
	}
}

func TestCreateRejectsWrongToken(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/resources", "errado", `{"idempotencyKey":"k1","name":"svc"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token errado: status = %d, quer 401", resp.StatusCode)
	}
}

func TestCreateProvisionsPostgres(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/resources", testToken,
		`{"kind":"postgres","idempotencyKey":"k1","name":"svc-abc","version":"16","region":"eu"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, quer 201", resp.StatusCode)
	}
	res := decodeResource(t, resp)
	if res.ID == "" || !strings.HasPrefix(res.ID, "pg-") {
		t.Fatalf("id inesperado: %q", res.ID)
	}
	if res.Kind != "postgres" {
		t.Fatalf("kind = %q, quer postgres", res.Kind)
	}
	if res.Status != "running" {
		t.Fatalf("status = %q, quer running", res.Status)
	}
	if res.Connection == nil {
		t.Fatal("connection ausente")
	}
	if !strings.HasPrefix(res.Connection.URL, "postgresql://app:") {
		t.Fatalf("url não começa com postgresql://app: → %q", res.Connection.URL)
	}
	if !strings.Contains(res.Connection.URL, "@10.0.0.5:") {
		t.Fatalf("url não usa advertise host: %q", res.Connection.URL)
	}
	if !strings.Contains(res.Connection.URL, "sslmode=require") {
		t.Fatalf("url sem sslmode=require: %q", res.Connection.URL)
	}
}

func TestCreateIsIdempotent(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	body := `{"kind":"postgres","idempotencyKey":"same-key","name":"svc"}`
	first := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken, body))
	second := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken, body))
	if first.ID != second.ID {
		t.Fatalf("idempotência quebrou: %q != %q", first.ID, second.ID)
	}
}

func TestCreateRejectsMissingIdempotencyKey(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/resources", testToken, `{"name":"svc"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, quer 400", resp.StatusCode)
	}
}

func TestCreateProvisionRedis(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/resources", testToken,
		`{"kind":"redis","idempotencyKey":"r1","name":"cache-abc","region":"eu"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, quer 201", resp.StatusCode)
	}
	res := decodeResource(t, resp)
	if !strings.HasPrefix(res.ID, "redis-") {
		t.Fatalf("id Redis inesperado: %q (quer prefixo redis-)", res.ID)
	}
	if res.Kind != "redis" {
		t.Fatalf("kind = %q, quer redis", res.Kind)
	}
	if res.Status != "running" {
		t.Fatalf("status = %q, quer running", res.Status)
	}
	if res.Connection == nil {
		t.Fatal("connection ausente")
	}
	if !strings.HasPrefix(res.Connection.URL, "redis://:") {
		t.Fatalf("URL Redis não começa com redis://: → %q", res.Connection.URL)
	}
	if !strings.Contains(res.Connection.URL, "@10.0.0.5:") {
		t.Fatalf("URL Redis não usa advertise host: %q", res.Connection.URL)
	}
}

// Validação de entrada na borda (achados HIGH-2/HIGH-3/LOW-10): campos que fluem
// para argumentos do Docker (label/filtro/tag) precisam de allowlist estrita.
func TestCreateRejectsMalformedInput(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	cases := []struct {
		name string
		body string
	}{
		{"idempotencyKey com espaço", `{"idempotencyKey":"k 1","name":"svc"}`},
		{"idempotencyKey com igual", `{"idempotencyKey":"k=evil","name":"svc"}`},
		{"idempotencyKey com newline", "{\"idempotencyKey\":\"k\\nx\",\"name\":\"svc\"}"},
		{"name com igual", `{"idempotencyKey":"k1","name":"a=b"}`},
		{"name com espaço", `{"idempotencyKey":"k1","name":"my svc"}`},
		{"region inválida", `{"idempotencyKey":"k1","name":"svc","region":"eu central"}`},
		{"version com shell-ish", `{"idempotencyKey":"k1","name":"svc","version":"16; rm -rf"}`},
		{"version com newline", "{\"idempotencyKey\":\"k1\",\"name\":\"svc\",\"version\":\"16\\nx\"}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, ts, "POST", "/v1/resources", testToken, tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, quer 400", resp.StatusCode)
			}
		})
	}
}

// Inverso: entradas válidas (incluindo name/region vazios e tag com ponto) passam.
func TestCreateAcceptsValidInput(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	cases := []string{
		`{"idempotencyKey":"org-1:svc-abc","name":"svc-abc","version":"16.2-alpine","region":"eu-central"}`,
		`{"idempotencyKey":"k1","name":""}`, // name vazio é permitido
	}
	for i, body := range cases {
		resp := do(t, ts, "POST", "/v1/resources", testToken, body)
		if resp.StatusCode != http.StatusCreated {
			resp.Body.Close()
			t.Fatalf("caso %d: status = %d, quer 201", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestGetReturnsResource(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	created := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken,
		`{"idempotencyKey":"k1","name":"svc"}`))

	resp := do(t, ts, "GET", "/v1/resources/"+created.ID, testToken, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, quer 200", resp.StatusCode)
	}
	got := decodeResource(t, resp)
	if got.ID != created.ID {
		t.Fatalf("id = %q, quer %q", got.ID, created.ID)
	}
}

func TestGetMissingReturns404(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "GET", "/v1/resources/pg-nao-existe", testToken, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, quer 404", resp.StatusCode)
	}
}

func TestStopAndStart(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	created := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken,
		`{"idempotencyKey":"k1","name":"svc"}`))

	stop := do(t, ts, "POST", "/v1/resources/"+created.ID+"/stop", testToken, "")
	stop.Body.Close()
	if stop.StatusCode != http.StatusAccepted {
		t.Fatalf("stop: status = %d, quer 202", stop.StatusCode)
	}
	afterStop := decodeResource(t, do(t, ts, "GET", "/v1/resources/"+created.ID, testToken, ""))
	if afterStop.Status != "stopped" {
		t.Fatalf("após stop status = %q, quer stopped", afterStop.Status)
	}

	start := do(t, ts, "POST", "/v1/resources/"+created.ID+"/start", testToken, "")
	start.Body.Close()
	if start.StatusCode != http.StatusAccepted {
		t.Fatalf("start: status = %d, quer 202", start.StatusCode)
	}
	afterStart := decodeResource(t, do(t, ts, "GET", "/v1/resources/"+created.ID, testToken, ""))
	if afterStart.Status != "running" {
		t.Fatalf("após start status = %q, quer running", afterStart.Status)
	}
}

func TestStopMissingReturns404(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/resources/pg-nao-existe/stop", testToken, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, quer 404", resp.StatusCode)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	created := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken,
		`{"idempotencyKey":"k1","name":"svc"}`))

	first := do(t, ts, "DELETE", "/v1/resources/"+created.ID, testToken, "")
	first.Body.Close()
	if first.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, quer 204", first.StatusCode)
	}
	// Segunda chamada (já removido) ainda é 204 — idempotente.
	second := do(t, ts, "DELETE", "/v1/resources/"+created.ID, testToken, "")
	second.Body.Close()
	if second.StatusCode != http.StatusNoContent {
		t.Fatalf("delete repetido: status = %d, quer 204", second.StatusCode)
	}
	// Após delete, GET dá 404.
	get := do(t, ts, "GET", "/v1/resources/"+created.ID, testToken, "")
	get.Body.Close()
	if get.StatusCode != http.StatusNotFound {
		t.Fatalf("get pós-delete: status = %d, quer 404", get.StatusCode)
	}
}

func TestHealthNeedsNoAuth(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "GET", "/healthz", "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, quer 200", resp.StatusCode)
	}
}
