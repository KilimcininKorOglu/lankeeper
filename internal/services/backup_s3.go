package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// s3Client speaks AWS Signature Version 4 against any S3-compatible
// endpoint (real S3, MinIO, Backblaze B2, DigitalOcean Spaces). We
// implement the minimum slice we actually use: PUT object, GET
// ListObjectsV2, DELETE object. Pure stdlib; no aws-sdk dependency.
type s3Client struct {
	Endpoint  string // https://s3.us-east-1.amazonaws.com
	Region    string // us-east-1
	AccessKey string
	SecretKey string
	PathStyle bool // true for MinIO et al.
	HTTP      *http.Client
}

func newS3Client(t config.BackupTarget) *s3Client {
	endpoint := t.Endpoint
	if endpoint == "" {
		endpoint = "https://s3." + t.Region + ".amazonaws.com"
	}
	endpoint = strings.TrimSuffix(endpoint, "/")
	return &s3Client{
		Endpoint:  endpoint,
		Region:    t.Region,
		AccessKey: t.AccessKeyID,
		SecretKey: t.SecretAccessKey,
		PathStyle: t.UsePathStyle,
		// Deliberately not a guarded client from safefetch.go. A
		// self-hosted S3 target (MinIO on the LAN or a NAS) is a
		// supported setup, and the guard refuses exactly those
		// addresses. The endpoint is written by the authenticated
		// operator on the backup page, not taken from a remote party.
		HTTP: &http.Client{Timeout: 30 * time.Minute},
	}
}

func (c *s3Client) bucketURL(bucket, key string) (string, string, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return "", "", err
	}
	if c.PathStyle {
		u.Path = "/" + bucket + "/" + key
		return u.String(), u.Host, nil
	}
	u.Host = bucket + "." + u.Host
	u.Path = "/" + key
	return u.String(), u.Host, nil
}

// putObject streams a file body to S3 with a precomputed SHA-256.
// We hash twice (once for the header, once for the signature) to
// avoid buffering the whole archive in memory. That's the
// canonical approach in the AWS docs and what the official SDK
// does internally for non-multipart uploads.
func (c *s3Client) putObject(ctx context.Context, bucket, key, srcPath string) error {
	hash, size, err := hashFileSHA256(srcPath)
	if err != nil {
		return fmt.Errorf("hash %s: %w", srcPath, err)
	}
	// srcPath is the archive this service just produced.
	// #nosec G304
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	endpoint, host, err := c.bucketURL(bucket, key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, f)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Host", host)
	req.Header.Set("x-amz-content-sha256", hash)
	if err := c.sign(req, hash); err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return s3Failure("PUT", key, resp)
	}
	return nil
}

type s3Object struct {
	Key          string
	LastModified time.Time
	Size         int64
}

func (c *s3Client) listObjects(ctx context.Context, bucket, prefix string) ([]s3Object, error) {
	endpoint, host, err := c.bucketURL(bucket, "")
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("list-type", "2")
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	emptyHash := sha256Hex(nil)
	req.Header.Set("Host", host)
	req.Header.Set("x-amz-content-sha256", emptyHash)
	if err := c.sign(req, emptyHash); err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, s3Failure("LIST", bucket, resp)
	}

	var parsed struct {
		Contents []struct {
			Key          string `xml:"Key"`
			LastModified string `xml:"LastModified"`
			Size         int64  `xml:"Size"`
		} `xml:"Contents"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]s3Object, 0, len(parsed.Contents))
	for _, e := range parsed.Contents {
		t, _ := time.Parse(time.RFC3339, e.LastModified)
		out = append(out, s3Object{Key: e.Key, LastModified: t, Size: e.Size})
	}
	return out, nil
}

func (c *s3Client) deleteObject(ctx context.Context, bucket, key string) error {
	endpoint, host, err := c.bucketURL(bucket, key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	emptyHash := sha256Hex(nil)
	req.Header.Set("Host", host)
	req.Header.Set("x-amz-content-sha256", emptyHash)
	if err := c.sign(req, emptyHash); err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return s3Failure("DELETE", key, resp)
	}
	return nil
}

// sign mutates req in place: adds x-amz-date, computes the SigV4
// authorization header, and attaches it. payloadHash is the hex
// SHA-256 of the body (or the empty-string hash for GET/DELETE).
func (c *s3Client) sign(req *http.Request, payloadHash string) error {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateOnly := now.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	if req.Header.Get("Host") == "" {
		req.Header.Set("Host", req.URL.Host)
	}

	// The query goes on the wire exactly as it is signed, so the server
	// canonicalises the same bytes.
	req.URL.RawQuery = canonicalQuery(req.URL)

	canonicalHeaders, signedHeaders := canonicalHeaders(req)
	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		canonicalQuery(req.URL),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateOnly, c.Region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+c.SecretKey), []byte(dateOnly))
	kRegion := hmacSHA256(kDate, []byte(c.Region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.AccessKey, scope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", auth)
	return nil
}

// canonicalHeaders builds the lower-cased, sorted, colon-joined
// header block plus the signed-headers list, both required by
// SigV4. We always include host and x-amz-date plus
// x-amz-content-sha256.
func canonicalHeaders(req *http.Request) (string, string) {
	type kv struct{ k, v string }
	var pairs []kv
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if lk == "host" || strings.HasPrefix(lk, "x-amz-") || lk == "content-type" {
			pairs = append(pairs, kv{lk, strings.TrimSpace(strings.Join(vs, ","))})
		}
	}
	if req.Header.Get("Host") == "" {
		pairs = append(pairs, kv{"host", req.URL.Host})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })

	var headerBuf, namesBuf strings.Builder
	for i, p := range pairs {
		headerBuf.WriteString(p.k)
		headerBuf.WriteString(":")
		headerBuf.WriteString(p.v)
		headerBuf.WriteString("\n")
		if i > 0 {
			namesBuf.WriteString(";")
		}
		namesBuf.WriteString(p.k)
	}
	return headerBuf.String(), namesBuf.String()
}

// canonicalQuery emits the URI-encoded, sorted, k=v&k=v form. Empty
// values are encoded as "k=" (a SigV4 quirk).
func canonicalQuery(u *url.URL) string {
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, sigvEscape(k)+"="+sigvEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// sigvEscape implements the RFC 3986 escaping SigV4 requires: unreserved
// characters stay as they are, in their own case, and every other byte
// becomes %XX with two uppercase hex digits. Go's url.QueryEscape uses
// '+' for a space, which the canonical form does not allow.
func sigvEscape(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func sha256Hex(p []byte) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func hashFileSHA256(path string) (string, int64, error) {
	// path is the archive this service just produced.
	// #nosec G304
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// uploadS3 uploads srcPath to t.Bucket/t.Prefix/<basename>. Returns
// the final S3 key. The caller logs/records this in history.
func uploadS3(ctx context.Context, srcPath string, t config.BackupTarget) (string, error) {
	if t.Bucket == "" {
		return "", fmt.Errorf("s3 bucket required")
	}
	if t.Region == "" {
		return "", fmt.Errorf("s3 region required")
	}
	c := newS3Client(t)
	key := strings.TrimPrefix(t.Prefix, "/")
	if key != "" && !strings.HasSuffix(key, "/") {
		key += "/"
	}
	key += filepathBase(srcPath)
	if err := c.putObject(ctx, t.Bucket, key, srcPath); err != nil {
		return "", err
	}
	return key, nil
}

// ownBackupObjects keeps only the lankeeper backup archives directly
// under prefix. The prefix is optional and a bucket can be shared, so
// every other key under it belongs to someone else and retention must
// never see it.
func ownBackupObjects(objects []s3Object, prefix string) []s3Object {
	var own []s3Object
	for _, o := range objects {
		name, ok := strings.CutPrefix(o.Key, prefix)
		if ok && !strings.Contains(name, "/") && strings.HasPrefix(name, "lankeeper-backup-") {
			own = append(own, o)
		}
	}
	return own
}

// cleanupS3 lists the lankeeper backup objects under the configured
// prefix, sorts them by LastModified descending, and deletes everything
// past `keep`.
func cleanupS3(ctx context.Context, t config.BackupTarget, keep int) ([]string, error) {
	if keep < 1 {
		return nil, fmt.Errorf("retention must be >= 1")
	}
	c := newS3Client(t)
	prefix := strings.TrimPrefix(t.Prefix, "/")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	listed, err := c.listObjects(ctx, t.Bucket, prefix)
	if err != nil {
		return nil, err
	}
	objects := ownBackupObjects(listed, prefix)
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].LastModified.After(objects[j].LastModified)
	})
	var deleted []string
	for i := keep; i < len(objects); i++ {
		if err := c.deleteObject(ctx, t.Bucket, objects[i].Key); err != nil {
			return deleted, err
		}
		deleted = append(deleted, objects[i].Key)
	}
	return deleted, nil
}

// filepathBase is a thin wrapper so backup_s3.go doesn't need to
// drag in the "path/filepath" import on top of "path". Equivalent
// to filepath.Base for forward-slash paths, which is what S3 uses.
func filepathBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// s3Failure turns a non-2xx response into an error the operator can act
// on, while keeping the provider's response body out of it.
//
// The body was previously embedded in the error text, and the backup
// page renders that error inline, so a provider's XML, which can name
// buckets, request IDs and account detail, ended up in the browser and
// in its history. The body still reaches the journal, which is where it
// is useful when diagnosing a failing target.
func s3Failure(op, target string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if len(body) > 0 {
		log.Printf("s3 %s %s: %s: %s", op, target, resp.Status, body)
	}
	return fmt.Errorf("s3 %s %s: %s", op, target, resp.Status)
}
