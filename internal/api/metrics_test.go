package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func decodeMetrics(t *testing.T, resp *http.Response) metricsResponse {
	t.Helper()
	defer resp.Body.Close()
	var out metricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decodificar métricas: %v", err)
	}
	return out
}

func TestMetricsRequiresAuth(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "GET", "/v1/resources/pg-x/metrics", "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sem token: status = %d, quer 401", resp.StatusCode)
	}
}

func TestMetricsNotFound(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "GET", "/v1/resources/inexistente/metrics", testToken, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("recurso ausente: status = %d, quer 404", resp.StatusCode)
	}
}

func TestMetricsReturnsSnapshot(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	created := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken,
		`{"kind":"postgres","idempotencyKey":"k1","name":"svc","version":"16","region":"eu"}`))

	resp := do(t, ts, "GET", "/v1/resources/"+created.ID+"/metrics", testToken, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, quer 200", resp.StatusCode)
	}
	m := decodeMetrics(t, resp)
	if m.ID != created.ID {
		t.Fatalf("id = %q, quer %q", m.ID, created.ID)
	}
	if m.Kind != "postgres" {
		t.Fatalf("kind = %q, quer postgres", m.Kind)
	}
	if m.Status != "running" {
		t.Fatalf("status = %q, quer running", m.Status)
	}
	if m.CPUPercent <= 0 || m.MemoryBytes <= 0 {
		t.Fatalf("métricas sintéticas vazias: cpu=%v mem=%d", m.CPUPercent, m.MemoryBytes)
	}
	if m.CollectedAt == "" {
		t.Fatalf("collectedAt vazio")
	}
}

func TestCreateAcceptsServerless(t *testing.T) {
	ts, fake := newTestServer()
	defer ts.Close()

	created := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken,
		`{"kind":"postgres","idempotencyKey":"k1","name":"svc","version":"16","region":"eu","serverless":true,"idleTimeoutSeconds":600}`))

	inst, err := fake.Get(context.Background(), created.ID)
	if err != nil || inst == nil {
		t.Fatalf("get: inst=%v err=%v", inst, err)
	}
	if !inst.Serverless {
		t.Fatalf("instância deveria ser serverless")
	}
	if inst.IdleTimeoutSeconds != 600 {
		t.Fatalf("idleTimeout = %d, quer 600", inst.IdleTimeoutSeconds)
	}
}

func TestCreateRejectsBadIdleTimeout(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/resources", testToken,
		`{"kind":"postgres","idempotencyKey":"k1","name":"svc","version":"16","idleTimeoutSeconds":999999}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("idleTimeout absurdo: status = %d, quer 400", resp.StatusCode)
	}
}
