package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const cloudflareAPIBase = "https://api.cloudflare.com/client/v4"

// maxCloudflareBody bounds what is read back from the API. The
// responses are a few hundred bytes; the cap is here so a compromised
// or misbehaving endpoint cannot make this process allocate without
// limit, matching how blocklist downloads are bounded.
const maxCloudflareBody = 1 << 20

var (
	ErrCloudflareTokenRequired = errors.New("a Cloudflare API token is required")
	ErrCloudflareZoneNotFound  = errors.New("no Cloudflare zone covers this domain")
)

// manualProvider asks the operator to publish the record themselves.
//
// It cannot wait for them: holding the request open for however long
// that takes would occupy a connection and a goroutine with no bound.
// Instead it reports the record and stops, and the operator runs
// issuance again once the record is live. The second run reuses the
// pending authorization the CA already created for this account and
// identifier, so the token, and therefore the record, is the same one.
type manualProvider struct{}

func (p *manualProvider) Present(_ context.Context, fqdn, value string) error {
	return &ManualChallengeError{Record: ManualRecord{Name: fqdn, Value: value}}
}

// CleanUp does nothing. The record was published by hand and this code
// has no way to withdraw it; saying so is better than pretending.
func (p *manualProvider) CleanUp(_ context.Context, _, _ string) error { return nil }

// cloudflareProvider publishes the record through the Cloudflare API.
type cloudflareProvider struct {
	apiBase string
	token   string
	// recordID is remembered between Present and CleanUp so the
	// withdrawal deletes the record this run created rather than
	// whatever a name lookup happens to return.
	recordID string
	zoneID   string
	// httpClient overrides the guarded client. Only a test sets it, and
	// only because the guard refuses loopback, which is where an
	// httptest server lives. Production leaves it nil and takes the
	// guarded path; TestCloudflareProductionPathIsGuarded holds that.
	httpClient *http.Client
}

// client returns the transport to use. The guarded one unless a test
// has swapped it, never a bare http.DefaultClient.
func (p *cloudflareProvider) client() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}
	return outboundFetchClient
}

type cloudflareEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

func (p *cloudflareProvider) Present(ctx context.Context, fqdn, value string) error {
	if strings.TrimSpace(p.token) == "" {
		return ErrCloudflareTokenRequired
	}

	zoneID, err := p.zoneFor(ctx, fqdn)
	if err != nil {
		return err
	}
	p.zoneID = zoneID

	body, err := json.Marshal(map[string]any{
		"type":    "TXT",
		"name":    fqdn,
		"content": value,
		// One minute, the Cloudflare minimum. The record exists for the
		// length of one validation and a long TTL only makes the next
		// attempt wait for a cached negative answer to age out.
		"ttl": 60,
	})
	if err != nil {
		return fmt.Errorf("encode record: %w", err)
	}

	raw, err := p.call(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", body)
	if err != nil {
		return fmt.Errorf("create TXT record: %w", err)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return fmt.Errorf("decode created record: %w", err)
	}
	p.recordID = created.ID
	return nil
}

func (p *cloudflareProvider) CleanUp(ctx context.Context, _, _ string) error {
	if p.recordID == "" || p.zoneID == "" {
		return nil
	}
	if _, err := p.call(ctx, http.MethodDelete, "/zones/"+p.zoneID+"/dns_records/"+p.recordID, nil); err != nil {
		return fmt.Errorf("delete TXT record: %w", err)
	}
	p.recordID = ""
	return nil
}

// zoneFor finds the zone that covers fqdn.
//
// The search walks the name from the left, so sub.example.co.uk tries
// sub.example.co.uk, example.co.uk, co.uk. Asking for the full name
// first and shortening means the most specific zone wins, which is what
// an account holding both a domain and a delegated subdomain needs.
func (p *cloudflareProvider) zoneFor(ctx context.Context, fqdn string) (string, error) {
	name := strings.TrimSuffix(fqdn, ".")
	name = strings.TrimPrefix(name, "_acme-challenge.")

	for {
		raw, err := p.call(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(name), nil)
		if err != nil {
			return "", fmt.Errorf("look up zone %q: %w", name, err)
		}
		var zones []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &zones); err != nil {
			return "", fmt.Errorf("decode zone list: %w", err)
		}
		if len(zones) > 0 {
			return zones[0].ID, nil
		}

		dot := strings.Index(name, ".")
		if dot < 0 {
			return "", fmt.Errorf("%w: %s", ErrCloudflareZoneNotFound, fqdn)
		}
		name = name[dot+1:]
	}
}

// call issues one API request through the guarded client.
func (p *cloudflareProvider) call(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.apiBase+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")

	// The guarded client, never a bare one: apiBase is settable and the
	// token would otherwise be sendable to whatever it names.
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCloudflareBody))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var env cloudflareEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode response (HTTP %d): %w", resp.StatusCode, err)
	}
	if !env.Success {
		// The API's own message, not the request. It names the
		// permission that was missing, which is the one thing the
		// operator needs and a bare status code does not carry.
		if len(env.Errors) > 0 {
			return nil, fmt.Errorf("cloudflare: %s (code %d)", env.Errors[0].Message, env.Errors[0].Code)
		}
		return nil, fmt.Errorf("cloudflare: request failed with HTTP %d", resp.StatusCode)
	}
	return env.Result, nil
}
