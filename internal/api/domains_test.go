package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adila/dash/worker/internal/builder"
	"github.com/adila/dash/worker/internal/runtime"
)

const boxIP = "46.225.134.88"

// staticResolver devolve sempre os mesmos endereços (DNS resolve para a box).
func staticResolver(addrs ...string) func(context.Context, string) ([]string, error) {
	return func(context.Context, string) ([]string, error) { return addrs, nil }
}

// newDomainsServer monta um Server roteado com resolver de DNS e IP público
// injetáveis, expondo o httptest e o runtime para os testes de domínios custom.
func newDomainsServer(t *testing.T, fr *fakeRouter, resolve func(context.Context, string) ([]string, error), publicIP string) (*httptest.Server, *runtime.Fake) {
	t.Helper()
	fake := runtime.NewFake()
	srv := NewServer(fake, builder.NewFake(), Config{
		Token:               testToken,
		AdvertiseHost:       "10.0.0.5",
		SSLMode:             "require",
		DefaultPgVersion:    "16",
		DefaultRedisVersion: "7",
		AppsBaseDomain:      "apps.adila.co",
		AppsPublicIP:        publicIP,
	}, nil, WithRouter(fr), func(s *Server) {
		if resolve != nil {
			s.resolveHost = resolve
		}
	})
	return httptest.NewServer(srv.Handler()), fake
}

// createApp sobe um app via POST /v1/resources e devolve o id gerado.
func createApp(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	res := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken, appBody))
	return res.ID
}

func customDomainsOf(t *testing.T, res resourceResponse) []string {
	t.Helper()
	raw, ok := res.Metadata["customDomains"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("customDomains não é lista: %T", raw)
	}
	out := make([]string, len(list))
	for i, v := range list {
		out[i] = v.(string)
	}
	return out
}

func TestSetDomainsHappyPath(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newDomainsServer(t, fr, staticResolver(boxIP), boxIP)
	defer ts.Close()
	id := createApp(t, ts)

	body := `{"domains":["lp.adila.co","www.lp.adila.co"]}`
	resp := do(t, ts, "PUT", "/v1/resources/"+id+"/domains", testToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, quer 200", resp.StatusCode)
	}
	res := decodeResource(t, resp)

	customs := customDomainsOf(t, res)
	if len(customs) != 2 || customs[0] != "lp.adila.co" || customs[1] != "www.lp.adila.co" {
		t.Fatalf("customDomains = %v, quer [lp.adila.co www.lp.adila.co]", customs)
	}

	// A rota foi reescrita com o subdomínio padrão + os customs, nessa ordem.
	last := fr.upserts[len(fr.upserts)-1]
	want := []string{id + ".apps.adila.co", "lp.adila.co", "www.lp.adila.co"}
	if len(last.domains) != len(want) {
		t.Fatalf("domínios da rota = %v, quer %v", last.domains, want)
	}
	for i := range want {
		if last.domains[i] != want[i] {
			t.Fatalf("domínio %d = %q, quer %q", i, last.domains[i], want[i])
		}
	}
}

func TestSetDomainsClearsWithEmptyList(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newDomainsServer(t, fr, staticResolver(boxIP), boxIP)
	defer ts.Close()
	id := createApp(t, ts)

	do(t, ts, "PUT", "/v1/resources/"+id+"/domains", testToken, `{"domains":["lp.adila.co"]}`).Body.Close()

	// Lista vazia limpa os customs, mantendo só o subdomínio padrão.
	resp := do(t, ts, "PUT", "/v1/resources/"+id+"/domains", testToken, `{"domains":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, quer 200", resp.StatusCode)
	}
	res := decodeResource(t, resp)
	if customs := customDomainsOf(t, res); len(customs) != 0 {
		t.Fatalf("esperava nenhum custom após limpar, obteve %v", customs)
	}
	last := fr.upserts[len(fr.upserts)-1]
	if len(last.domains) != 1 || last.domains[0] != id+".apps.adila.co" {
		t.Fatalf("rota após limpar = %v, quer [%s.apps.adila.co]", last.domains, id)
	}
}

func TestSetDomainsRejectsInvalidFormat(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newDomainsServer(t, fr, staticResolver(boxIP), boxIP)
	defer ts.Close()
	id := createApp(t, ts)

	resp := do(t, ts, "PUT", "/v1/resources/"+id+"/domains", testToken, `{"domains":["dominio invalido"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, quer 400", resp.StatusCode)
	}
}

func TestSetDomainsRejectsBaseDomain(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newDomainsServer(t, fr, staticResolver(boxIP), boxIP)
	defer ts.Close()
	id := createApp(t, ts)

	// Domínio sob o base-domain da plataforma é gerenciado pelo agent → 400.
	resp := do(t, ts, "PUT", "/v1/resources/"+id+"/domains", testToken, `{"domains":["roubado.apps.adila.co"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, quer 400", resp.StatusCode)
	}
}

func TestSetDomainsRejectsDuplicate(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newDomainsServer(t, fr, staticResolver(boxIP), boxIP)
	defer ts.Close()
	id := createApp(t, ts)

	resp := do(t, ts, "PUT", "/v1/resources/"+id+"/domains", testToken, `{"domains":["lp.adila.co","lp.adila.co"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, quer 400", resp.StatusCode)
	}
}

func TestSetDomainsDNSMismatchReturns422(t *testing.T) {
	fr := &fakeRouter{}
	// O domínio resolve, mas para outro IP que não a box → 422.
	ts, _ := newDomainsServer(t, fr, staticResolver("1.2.3.4"), boxIP)
	defer ts.Close()
	id := createApp(t, ts)

	resp := do(t, ts, "PUT", "/v1/resources/"+id+"/domains", testToken, `{"domains":["lp.adila.co"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, quer 422", resp.StatusCode)
	}
	if len(fr.upserts) != 1 {
		t.Fatalf("DNS errado não deveria reescrever a rota; upserts=%d", len(fr.upserts))
	}
}

func TestSetDomainsDNSUnresolvedReturns422(t *testing.T) {
	fr := &fakeRouter{}
	resolveErr := func(context.Context, string) ([]string, error) { return nil, context.DeadlineExceeded }
	ts, _ := newDomainsServer(t, fr, resolveErr, boxIP)
	defer ts.Close()
	id := createApp(t, ts)

	resp := do(t, ts, "PUT", "/v1/resources/"+id+"/domains", testToken, `{"domains":["lp.adila.co"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, quer 422", resp.StatusCode)
	}
}

func TestSetDomainsWithoutPublicIPOnlyRequiresResolution(t *testing.T) {
	fr := &fakeRouter{}
	// Sem AppsPublicIP: basta o domínio resolver para qualquer endereço.
	ts, _ := newDomainsServer(t, fr, staticResolver("203.0.113.7"), "")
	defer ts.Close()
	id := createApp(t, ts)

	resp := do(t, ts, "PUT", "/v1/resources/"+id+"/domains", testToken, `{"domains":["lp.adila.co"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, quer 200", resp.StatusCode)
	}
}

func TestSetDomainsNotApp(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newDomainsServer(t, fr, staticResolver(boxIP), boxIP)
	defer ts.Close()

	pg := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken,
		`{"kind":"postgres","idempotencyKey":"kpg","name":"db"}`))
	resp := do(t, ts, "PUT", "/v1/resources/"+pg.ID+"/domains", testToken, `{"domains":["lp.adila.co"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, quer 400 (domínio custom só para app)", resp.StatusCode)
	}
}

func TestSetDomainsNotFound(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newDomainsServer(t, fr, staticResolver(boxIP), boxIP)
	defer ts.Close()

	resp := do(t, ts, "PUT", "/v1/resources/app-inexistente/domains", testToken, `{"domains":["lp.adila.co"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, quer 404", resp.StatusCode)
	}
}

func TestSetDomainsRoutingOff(t *testing.T) {
	// Server sem AppsBaseDomain: roteamento desligado → 409.
	fake := runtime.NewFake()
	srv := NewServer(fake, builder.NewFake(), Config{
		Token:         testToken,
		AdvertiseHost: "10.0.0.5",
		SSLMode:       "require",
	}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	id := createApp(t, ts)
	resp := do(t, ts, "PUT", "/v1/resources/"+id+"/domains", testToken, `{"domains":["lp.adila.co"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, quer 409", resp.StatusCode)
	}
}

func TestSetDomainsRequiresAuth(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newDomainsServer(t, fr, staticResolver(boxIP), boxIP)
	defer ts.Close()

	resp := do(t, ts, "PUT", "/v1/resources/app-x/domains", "token-errado", `{"domains":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, quer 401", resp.StatusCode)
	}
}

func TestSetDomainsRejectsTooMany(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newDomainsServer(t, fr, staticResolver(boxIP), boxIP)
	defer ts.Close()
	id := createApp(t, ts)

	body := `{"domains":["a1.x.co","a2.x.co","a3.x.co","a4.x.co","a5.x.co","a6.x.co","a7.x.co","a8.x.co","a9.x.co","a10.x.co","a11.x.co"]}`
	resp := do(t, ts, "PUT", "/v1/resources/"+id+"/domains", testToken, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, quer 400 (acima do limite)", resp.StatusCode)
	}
}

func TestGetAppIncludesCustomDomains(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newDomainsServer(t, fr, staticResolver(boxIP), boxIP)
	defer ts.Close()
	id := createApp(t, ts)

	do(t, ts, "PUT", "/v1/resources/"+id+"/domains", testToken, `{"domains":["lp.adila.co"]}`).Body.Close()

	res := decodeResource(t, do(t, ts, "GET", "/v1/resources/"+id, testToken, ""))
	customs := customDomainsOf(t, res)
	if len(customs) != 1 || customs[0] != "lp.adila.co" {
		t.Fatalf("GET deveria refletir o domínio custom, obteve %v", customs)
	}
}
