package services

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSigV4SignatureFormat exercises the canonical request +
// authorization header format. We don't reproduce the AWS
// known-test-suite vectors verbatim (that would require freezing
// a date stamp which clashes with our time.Now path), but we
// assert structural invariants that any compliant signer must
// preserve: AWS4-HMAC-SHA256 prefix, scope contains date/region/s3,
// SignedHeaders is sorted lower-case semicolon-joined, signature
// is 64-hex.
func TestSigV4SignatureFormat(t *testing.T) {
	c := &s3Client{
		Endpoint:  "https://s3.us-east-1.amazonaws.com",
		Region:    "us-east-1",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}
	req, err := http.NewRequest("PUT", "https://test-bucket.s3.us-east-1.amazonaws.com/foo.txt", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Host", "test-bucket.s3.us-east-1.amazonaws.com")
	req.Header.Set("x-amz-content-sha256", "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5")

	if err := c.sign(req, req.Header.Get("x-amz-content-sha256")); err != nil {
		t.Fatal(err)
	}

	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Errorf("auth header prefix wrong: %s", auth)
	}
	if !strings.Contains(auth, "Credential=AKIAIOSFODNN7EXAMPLE/") {
		t.Errorf("auth missing credential: %s", auth)
	}
	if !strings.Contains(auth, "/us-east-1/s3/aws4_request") {
		t.Errorf("auth scope wrong: %s", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Errorf("signed headers wrong: %s", auth)
	}
	// Signature is hex SHA-256 → 64 chars.
	_, after, ok := strings.Cut(auth, "Signature=")
	if !ok {
		t.Fatalf("no signature: %s", auth)
	}
	sig := after
	if len(sig) != 64 {
		t.Errorf("signature len = %d, want 64", len(sig))
	}
	if req.Header.Get("x-amz-date") == "" {
		t.Error("x-amz-date not set")
	}
}

// SigV4 keeps unreserved characters in their own case and writes every
// escape as two uppercase hex digits. The server recomputes the same
// string from the query it receives, so any other case fails the
// signature.
func TestSigvEscape(t *testing.T) {
	cases := map[string]string{
		"list-type":   "list-type",
		"hello world": "hello%20world",
		"a/b":         "a%2Fb",
		"a~b._-":      "a~b._-",
		"\t":          "%09",
		"\xff":        "%FF",
	}
	for in, want := range cases {
		if got := sigvEscape(in); got != want {
			t.Errorf("sigvEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

// The query must go on the wire in the form that was signed.
func TestSignSendsTheCanonicalQuery(t *testing.T) {
	c := &s3Client{Region: "us-east-1", AccessKey: "AK", SecretKey: "SK"}
	req, err := http.NewRequest(http.MethodGet, "https://s3.example/bucket?prefix=a+b/c&list-type=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.sign(req, sha256Hex(nil)); err != nil {
		t.Fatal(err)
	}
	if want := "list-type=2&prefix=a%20b%2Fc"; req.URL.RawQuery != want {
		t.Fatalf("wire query = %q, want %q", req.URL.RawQuery, want)
	}
}

func TestS3ListObjectsParsesXML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult>
  <Contents>
    <Key>backup1.tar</Key>
    <LastModified>2026-05-07T03:00:00Z</LastModified>
    <Size>1024</Size>
  </Contents>
  <Contents>
    <Key>backup2.tar</Key>
    <LastModified>2026-05-08T03:00:00Z</LastModified>
    <Size>2048</Size>
  </Contents>
</ListBucketResult>`))
	}))
	defer server.Close()

	c := &s3Client{
		Endpoint:  server.URL,
		Region:    "us-east-1",
		AccessKey: "key",
		SecretKey: "secret",
		PathStyle: true,
		HTTP:      server.Client(),
	}
	objects, err := c.listObjects(t.Context(), "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(objects))
	}
	if objects[1].Size != 2048 {
		t.Errorf("size = %d", objects[1].Size)
	}
}

// TestOwnBackupObjectsKeepsOnlyBackupsDirectlyUnderPrefix pins the
// retention filter. The prefix is optional and a bucket can be shared,
// so retention used to delete every other object in the bucket.
func TestOwnBackupObjectsKeepsOnlyBackupsDirectlyUnderPrefix(t *testing.T) {
	objects := []s3Object{
		{Key: "lankeeper-backup-1.tar.gz"},
		{Key: "photos/cat.jpg"},
		{Key: "report.pdf"},
		{Key: "other/lankeeper-backup-2.tar.gz"},
	}
	got := ownBackupObjects(objects, "")
	if len(got) != 1 || got[0].Key != "lankeeper-backup-1.tar.gz" {
		t.Errorf("empty prefix: got %v, want only the root backup", got)
	}
	got = ownBackupObjects(objects, "other/")
	if len(got) != 1 || got[0].Key != "other/lankeeper-backup-2.tar.gz" {
		t.Errorf("prefix other/: got %v, want only other/lankeeper-backup-2.tar.gz", got)
	}
}
