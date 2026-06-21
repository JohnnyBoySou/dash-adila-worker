package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func decodeLogs(t *testing.T, resp *http.Response) logsResponse {
	t.Helper()
	defer resp.Body.Close()
	var out logsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decodificar logs: %v", err)
	}
	return out
}

func TestLogsRequiresAuth(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "GET", "/v1/resources/pg-x/logs", "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sem token: status = %d, quer 401", resp.StatusCode)
	}
}

func TestLogsNotFound(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "GET", "/v1/resources/inexistente/logs", testToken, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("recurso ausente: status = %d, quer 404", resp.StatusCode)
	}
}

func TestLogsReturnsLines(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	created := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken,
		`{"kind":"app","idempotencyKey":"k1","name":"svc","image":"nginx:1.27","region":"eu"}`))

	resp := do(t, ts, "GET", "/v1/resources/"+created.ID+"/logs", testToken, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, quer 200", resp.StatusCode)
	}
	logs := decodeLogs(t, resp)
	if len(logs.Lines) == 0 {
		t.Fatalf("nenhuma linha de log devolvida")
	}
	for i, l := range logs.Lines {
		if l.Message == "" {
			t.Fatalf("linha %d com mensagem vazia", i)
		}
		if l.Timestamp == "" {
			t.Fatalf("linha %d sem timestamp", i)
		}
	}
}

func TestLogsRespectsTail(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	created := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken,
		`{"kind":"app","idempotencyKey":"k1","name":"svc","image":"nginx:1.27","region":"eu"}`))

	resp := do(t, ts, "GET", "/v1/resources/"+created.ID+"/logs?tail=1", testToken, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, quer 200", resp.StatusCode)
	}
	logs := decodeLogs(t, resp)
	if len(logs.Lines) != 1 {
		t.Fatalf("tail=1: %d linhas, quer 1", len(logs.Lines))
	}
}

func TestLogsRejectsBadTail(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	created := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken,
		`{"kind":"app","idempotencyKey":"k1","name":"svc","image":"nginx:1.27","region":"eu"}`))

	for _, q := range []string{"?tail=-1", "?tail=99999", "?tail=abc", "?since=ontem"} {
		resp := do(t, ts, "GET", "/v1/resources/"+created.ID+"/logs"+q, testToken, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("query %q: status = %d, quer 400", q, resp.StatusCode)
		}
	}
}
