package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/adila/dash/worker/internal/builder"
	"github.com/adila/dash/worker/internal/runtime"
)

// fakeRouter registra as chamadas de Upsert/Remove e permite injetar falha, para
// verificar o wiring do roteamento sem tocar no Caddy real.
type fakeRouter struct {
	mu        sync.Mutex
	upserts   []routeCall
	removes   []string
	upsertErr error
	removeErr error
}

type routeCall struct {
	id     string
	domain string
	port   int
}

func (f *fakeRouter) Upsert(_ context.Context, id, domain string, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upserts = append(f.upserts, routeCall{id, domain, port})
	return nil
}

func (f *fakeRouter) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removes = append(f.removes, id)
	return nil
}

// newRoutedServer monta um Server com roteamento ligado (baseDomain) e o fakeRouter.
func newRoutedServer(t *testing.T, baseDomain string, fr *fakeRouter) (*httptest.Server, *runtime.Fake) {
	t.Helper()
	fake := runtime.NewFake()
	srv := NewServer(fake, builder.NewFake(), Config{
		Token:               testToken,
		AdvertiseHost:       "10.0.0.5",
		SSLMode:             "require",
		DefaultPgVersion:    "16",
		DefaultRedisVersion: "7",
		AppsBaseDomain:      baseDomain,
	}, nil, WithRouter(fr))
	return httptest.NewServer(srv.Handler()), fake
}

const appBody = `{"kind":"app","idempotencyKey":"k1","name":"myapp","region":"eu","image":"registry-dash.adila.co/myapp:1","containerPort":8080}`

func TestCreateAppRegistersRouteAndReturnsHTTPSURL(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newRoutedServer(t, "apps.adila.co", fr)
	defer ts.Close()

	res := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken, appBody))
	if !strings.HasPrefix(res.ID, "app-") {
		t.Fatalf("id inesperado: %q", res.ID)
	}
	if res.Connection == nil {
		t.Fatal("connection ausente para app")
	}
	wantURL := "https://" + res.ID + ".apps.adila.co"
	if res.Connection.URL != wantURL {
		t.Fatalf("URL pública = %q, quer %q", res.Connection.URL, wantURL)
	}

	if len(fr.upserts) != 1 {
		t.Fatalf("esperava 1 Upsert, obteve %d", len(fr.upserts))
	}
	call := fr.upserts[0]
	if call.id != res.ID || call.domain != res.ID+".apps.adila.co" || call.port == 0 {
		t.Fatalf("Upsert chamado com argumentos inesperados: %+v", call)
	}
}

func TestCreateAppRouteFailureReturns500(t *testing.T) {
	fr := &fakeRouter{upsertErr: errors.New("caddy fora do ar")}
	ts, _ := newRoutedServer(t, "apps.adila.co", fr)
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/resources", testToken, appBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("falha de rota: status = %d, quer 500", resp.StatusCode)
	}
}

func TestDeleteAppRemovesRoute(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newRoutedServer(t, "apps.adila.co", fr)
	defer ts.Close()

	res := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken, appBody))

	resp := do(t, ts, "DELETE", "/v1/resources/"+res.ID, testToken, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, quer 204", resp.StatusCode)
	}
	if len(fr.removes) != 1 || fr.removes[0] != res.ID {
		t.Fatalf("Remove não chamado corretamente: %+v", fr.removes)
	}
}

func TestDeleteNonAppSkipsRouteRemoval(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newRoutedServer(t, "apps.adila.co", fr)
	defer ts.Close()

	// Postgres não tem rota pública — o delete não deve tocar no router.
	pg := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken,
		`{"kind":"postgres","idempotencyKey":"k2","name":"db"}`))
	resp := do(t, ts, "DELETE", "/v1/resources/"+pg.ID, testToken, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete pg: status = %d, quer 204", resp.StatusCode)
	}
	if len(fr.removes) != 0 {
		t.Fatalf("Remove não deveria ser chamado para Postgres: %+v", fr.removes)
	}
}

func TestDeleteAppRouteFailureReturns500(t *testing.T) {
	fr := &fakeRouter{}
	ts, _ := newRoutedServer(t, "apps.adila.co", fr)
	defer ts.Close()
	res := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken, appBody))

	fr.removeErr = errors.New("reload falhou")
	resp := do(t, ts, "DELETE", "/v1/resources/"+res.ID, testToken, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("falha ao remover rota: status = %d, quer 500", resp.StatusCode)
	}
}

func TestCreateAppWithoutRoutingUsesLoopbackURL(t *testing.T) {
	// Sem AppsBaseDomain o roteamento fica off: URL cai no http://advertise:port e o
	// router (default NoopRouter) não é exercitado.
	fake := runtime.NewFake()
	srv := NewServer(fake, builder.NewFake(), Config{
		Token:         testToken,
		AdvertiseHost: "10.0.0.5",
		SSLMode:       "require",
	}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	res := decodeResource(t, do(t, ts, "POST", "/v1/resources", testToken, appBody))
	if res.Connection == nil {
		t.Fatal("connection ausente")
	}
	if !strings.HasPrefix(res.Connection.URL, "http://10.0.0.5:") {
		t.Fatalf("sem roteamento esperava URL loopback, obteve %q", res.Connection.URL)
	}
}
