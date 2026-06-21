package api

import (
	"net/http"
	"testing"
)

// Volumes válidos no deploy de um app são aceitos (201).
func TestCreateAppAcceptsValidVolumes(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/resources", testToken,
		`{"kind":"app","idempotencyKey":"v1","name":"web","region":"eu","image":"nginx:1.27",
		  "volumes":[{"id":"vol-1","mountPath":"/data"},{"id":"vol-2","mountPath":"/cache"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, quer 201", resp.StatusCode)
	}
}

// Cada classe de volume malformado é rejeitada na borda (400) — id, mountPath
// relativo, travessia, separador `:`, duplicata de mountPath e acima do limite.
func TestCreateAppRejectsBadVolumes(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	cases := []struct {
		name string
		body string
	}{
		{"id inválido", `{"kind":"app","idempotencyKey":"k","name":"s","image":"nginx","volumes":[{"id":"bad id","mountPath":"/data"}]}`},
		{"mountPath relativo", `{"kind":"app","idempotencyKey":"k","name":"s","image":"nginx","volumes":[{"id":"vol-1","mountPath":"data"}]}`},
		{"mountPath com travessia", `{"kind":"app","idempotencyKey":"k","name":"s","image":"nginx","volumes":[{"id":"vol-1","mountPath":"/data/../etc"}]}`},
		{"mountPath com dois-pontos", `{"kind":"app","idempotencyKey":"k","name":"s","image":"nginx","volumes":[{"id":"vol-1","mountPath":"/data:x"}]}`},
		{"mountPath duplicado", `{"kind":"app","idempotencyKey":"k","name":"s","image":"nginx","volumes":[{"id":"vol-1","mountPath":"/data"},{"id":"vol-2","mountPath":"/data"}]}`},
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

// DELETE /v1/volumes/{id} encaminha a exclusão ao runtime e responde 204.
func TestDeleteVolumeForwardsToRuntime(t *testing.T) {
	ts, fake := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "DELETE", "/v1/volumes/vol-abc", testToken, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, quer 204", resp.StatusCode)
	}
	if len(fake.DeletedVolumes) != 1 || fake.DeletedVolumes[0] != "vol-abc" {
		t.Fatalf("DeletedVolumes = %v, quer [vol-abc]", fake.DeletedVolumes)
	}
}

// Id de volume malformado é barrado na borda (400) — não chega ao runtime.
func TestDeleteVolumeRejectsBadID(t *testing.T) {
	ts, fake := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "DELETE", "/v1/volumes/bad%20id", testToken, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, quer 400", resp.StatusCode)
	}
	if len(fake.DeletedVolumes) != 0 {
		t.Fatalf("DeletedVolumes = %v, quer vazio", fake.DeletedVolumes)
	}
}

// O endpoint exige autenticação bearer.
func TestDeleteVolumeRequiresAuth(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()

	resp := do(t, ts, "DELETE", "/v1/volumes/vol-abc", "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, quer 401", resp.StatusCode)
	}
}
