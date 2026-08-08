package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// fakeCloudflare stands in for the v4 API. It records what it was asked
// so the test can assert on the request shaping, which is the part this
// code owns; the API's own behaviour is not under test.
type fakeCloudflare struct {
	mu sync.Mutex
	// zones maps a zone name to its id. A name that is absent answers
	// with an empty result, which is what drives the suffix walk.
	zones map[string]string
	// createFails makes the record POST answer with a Cloudflare-shaped
	// error, which is how a token missing DNS:Edit comes back.
	createFails bool

	authHeaders []string
	zoneQueries []string
	created     map[string]any
	deleted     []string
}

func (f *fakeCloudflare) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			name := r.URL.Query().Get("name")
			f.zoneQueries = append(f.zoneQueries, name)
			if id, ok := f.zones[name]; ok {
				_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + id + `"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"result":[]}`))

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dns_records"):
			if f.createFails {
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}],"result":null}`))
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.created = body
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"rec-1"}}`))

		case r.Method == http.MethodDelete:
			f.deleted = append(f.deleted, r.URL.Path)
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"rec-1"}}`))

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":7003,"message":"no route"}]}`))
		}
	})
}

func TestCloudflarePublishesAndWithdrawsTheChallengeRecord(t *testing.T) {
	fake := &fakeCloudflare{zones: map[string]string{"example.com": "zone-1"}}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	p := &cloudflareProvider{apiBase: srv.URL, token: "tok", httpClient: srv.Client()}
	fqdn := "_acme-challenge.hermes.example.com"

	if err := p.Present(context.Background(), fqdn, "challenge-value"); err != nil {
		t.Fatalf("present: %v", err)
	}

	if got := fake.created["name"]; got != fqdn {
		t.Errorf("record name = %v, want %s", got, fqdn)
	}
	if got := fake.created["type"]; got != "TXT" {
		t.Errorf("record type = %v, want TXT", got)
	}
	if got := fake.created["content"]; got != "challenge-value" {
		t.Errorf("record content = %v, want the challenge value", got)
	}

	if err := p.CleanUp(context.Background(), fqdn, "challenge-value"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if len(fake.deleted) != 1 || !strings.HasSuffix(fake.deleted[0], "/zones/zone-1/dns_records/rec-1") {
		t.Errorf("deleted = %v, want the record this run created", fake.deleted)
	}
	// Deleting by the remembered id, not by a name lookup: a lookup
	// would be free to match a record somebody else put there.
	if p.recordID != "" {
		t.Error("the record id survived cleanup, so a second cleanup would delete it again")
	}
}

// The zone is not the challenge name. Walking from the left means an
// account holding both a domain and a delegated subdomain gets the most
// specific zone, which is the one whose token is scoped to it.
func TestCloudflareFindsTheZoneBySuffix(t *testing.T) {
	fake := &fakeCloudflare{zones: map[string]string{"example.com": "zone-1"}}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	p := &cloudflareProvider{apiBase: srv.URL, token: "tok", httpClient: srv.Client()}
	if err := p.Present(context.Background(), "_acme-challenge.a.b.example.com", "v"); err != nil {
		t.Fatalf("present: %v", err)
	}

	want := []string{"a.b.example.com", "b.example.com", "example.com"}
	if len(fake.zoneQueries) != len(want) {
		t.Fatalf("zone queries = %v, want %v", fake.zoneQueries, want)
	}
	for i, q := range want {
		if fake.zoneQueries[i] != q {
			t.Errorf("query %d = %q, want %q", i, fake.zoneQueries[i], q)
		}
	}
	if p.zoneID != "zone-1" {
		t.Errorf("zone = %q, want zone-1", p.zoneID)
	}
}

func TestCloudflareReportsAZoneItCannotFind(t *testing.T) {
	fake := &fakeCloudflare{zones: map[string]string{}}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	p := &cloudflareProvider{apiBase: srv.URL, token: "tok", httpClient: srv.Client()}
	err := p.Present(context.Background(), "_acme-challenge.hermes.example.com", "v")
	if !errors.Is(err, ErrCloudflareZoneNotFound) {
		t.Fatalf("error = %v, want ErrCloudflareZoneNotFound", err)
	}
}

// The API answers HTTP 200 with success:false for a token that lacks a
// permission. Reading only the status code would treat that as a
// published record and hand the CA a name that resolves to nothing.
func TestCloudflareTreatsAnUnsuccessfulEnvelopeAsAFailure(t *testing.T) {
	fake := &fakeCloudflare{zones: map[string]string{"example.com": "zone-1"}, createFails: true}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	p := &cloudflareProvider{apiBase: srv.URL, token: "tok", httpClient: srv.Client()}
	err := p.Present(context.Background(), "_acme-challenge.hermes.example.com", "v")
	if err == nil {
		t.Fatal("a success:false response was treated as a published record")
	}
	if !strings.Contains(err.Error(), "Authentication error") {
		t.Errorf("the API's own message did not reach the operator: %v", err)
	}
	if p.recordID != "" {
		t.Error("a record id was recorded for a create that failed")
	}
}

// The token authenticates against the whole zone, so a missing one has
// to stop the flow rather than produce an unauthenticated request.
func TestCloudflareRefusesWithoutAToken(t *testing.T) {
	fake := &fakeCloudflare{zones: map[string]string{"example.com": "zone-1"}}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	p := &cloudflareProvider{apiBase: srv.URL, token: "  ", httpClient: srv.Client()}
	if err := p.Present(context.Background(), "_acme-challenge.hermes.example.com", "v"); !errors.Is(err, ErrCloudflareTokenRequired) {
		t.Fatalf("error = %v, want ErrCloudflareTokenRequired", err)
	}
	if len(fake.authHeaders) != 0 {
		t.Error("a request went out without a token")
	}
}

func TestCloudflareSendsTheTokenAsABearerCredential(t *testing.T) {
	fake := &fakeCloudflare{zones: map[string]string{"example.com": "zone-1"}}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	p := &cloudflareProvider{apiBase: srv.URL, token: "tok-123", httpClient: srv.Client()}
	if err := p.Present(context.Background(), "_acme-challenge.example.com", "v"); err != nil {
		t.Fatalf("present: %v", err)
	}
	for i, h := range fake.authHeaders {
		if h != "Bearer tok-123" {
			t.Errorf("request %d sent Authorization %q, want a bearer token", i, h)
		}
	}
}

// The manual provider cannot hold the request open waiting for a human,
// so it reports the record and stops. The record has to travel with the
// error, or the page has nothing to show.
func TestManualProviderReturnsTheRecordToPublish(t *testing.T) {
	p := &manualProvider{}

	err := p.Present(context.Background(), "_acme-challenge.hermes.example", "abc123")
	if !errors.Is(err, ErrManualRecordPending) {
		t.Fatalf("error = %v, want ErrManualRecordPending", err)
	}

	var manual *ManualChallengeError
	if !errors.As(err, &manual) {
		t.Fatal("the error does not carry the record")
	}
	if manual.Record.Name != "_acme-challenge.hermes.example" || manual.Record.Value != "abc123" {
		t.Errorf("record = %+v, want the challenge name and value", manual.Record)
	}

	// Nothing to withdraw: the record was published by hand and this
	// code has no way to remove it. Reporting success would be a claim
	// it cannot make, but an error would abort a flow that is fine.
	if err := p.CleanUp(context.Background(), "_acme-challenge.hermes.example", "abc123"); err != nil {
		t.Errorf("cleanup = %v, want nil", err)
	}
}

// Every outbound call has to go through the guarded client. A bare one
// would let a directory or API base naming an internal address reach the
// router's own services, and the token would go with it.
func TestCloudflareUsesTheGuardedClient(t *testing.T) {
	p := &cloudflareProvider{apiBase: "http://127.0.0.1:1", token: "tok"}

	err := p.Present(context.Background(), "_acme-challenge.example.com", "v")
	if err == nil {
		t.Fatal("a request to a loopback address succeeded")
	}
	if !strings.Contains(err.Error(), "internal address") {
		t.Errorf("the request was refused for the wrong reason: %v", err)
	}
}

// The override exists only for the tests above. If the service ever
// hands one in, the guard stops applying to the request that carries the
// zone-wide token, and nothing else in the tree would notice.
func TestCloudflareProductionPathIsGuarded(t *testing.T) {
	svc := &ACMEService{
		cfg:           newTLSConfigForProviderTest(),
		cloudflareAPI: cloudflareAPIBase,
	}
	svc.cfg.System.TLS.ACME.DNSChallenge.Provider = "cloudflare"

	prov, err := svc.provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	cf, ok := prov.(*cloudflareProvider)
	if !ok {
		t.Fatalf("provider = %T, want *cloudflareProvider", prov)
	}
	if cf.httpClient != nil {
		t.Error("the service wired an explicit client, so the SSRF guard no longer covers the API call")
	}
	if cf.client() != outboundFetchClient {
		t.Error("the provider does not use the guarded client by default")
	}
}

func newTLSConfigForProviderTest() *config.Config {
	return &config.Config{}
}
