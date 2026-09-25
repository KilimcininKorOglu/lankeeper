package services

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// TestSixInFourDDNSClientRefusesInternalAddresses is the regression
// test. The DDNS call used a bare http.Client, so a poisoned answer for
// the HE.net host or a redirect sent the request, with the tunnel
// username and update key in its Authorization header, to any address
// behind the router.
func TestSixInFourDDNSClientRefusesInternalAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("good 203.0.113.9"))
	}))
	defer srv.Close()

	resp, err := NewSixInFourService(&config.Config{}).httpClient.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the DDNS client reached a loopback server")
	}
	if !strings.Contains(err.Error(), "refusing to connect to internal address") {
		t.Errorf("err = %v, want the internal-address refusal", err)
	}
}
