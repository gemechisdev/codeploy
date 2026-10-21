// Package objectstore is the S3-compatible object port the backup module writes dumps to. It speaks plain
// Signature V4 over net/http, which MinIO, DigitalOcean Spaces, Scaleway and AWS S3 all accept, so the panel
// takes on no cloud SDK. Addressing is path style (endpoint/bucket/key) everywhere, including s3.amazonaws.com:
// virtual-host style needs the bucket to resolve as a subdomain of the endpoint, which the self-hosted and
// regional endpoints this panel is configured with generally do not offer. Payload hashes arrive from the caller
// instead of being recomputed, because a dump is streamed once and cannot be rewound.
package objectstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	service        = "s3"
	algorithm      = "AWS4-HMAC-SHA256"
	requestTerm    = "aws4_request"
	signedHeaders  = "host;x-amz-content-sha256;x-amz-date"
	amzDateFormat  = "20060102T150405Z"
	defaultTimeout = 120 * time.Second
	// An S3 error body is XML holding the reason (SignatureDoesNotMatch, NoSuchBucket, AccessDenied), which the
	// operator needs to see; the first few hundred bytes carry it, and the cap bounds memory for the case where
	// a proxy replies with an HTML error page instead.
	errorBodyLimit = 512
	sha256HexLen   = 64
)

// emptyPayloadHash is SHA-256 of zero bytes: GET, HEAD and DELETE send no body, and SigV4 still hashes one.
var emptyPayloadHash = hexSHA256(nil)

// Config is one S3-compatible destination.
type Config struct {
	Endpoint  string // "https://s3.fr-par.scw.cloud" or "https://s3.amazonaws.com"; empty means the AWS default regional endpoint
	Region    string // required: it is inside the credential scope, so a wrong one fails as an unhelpful SignatureDoesNotMatch
	Bucket    string
	AccessKey string
	SecretKey string
	Prefix    string // optional key prefix inside the bucket, e.g. "codeplo/backups"
}

// Client is one bucket. It is safe for concurrent use.
type Client struct {
	// HTTP replaces the transport. The default bounds a request at 120 seconds, which a multi-gigabyte dump
	// can legitimately pass: a caller uploading big objects sets its own client here.
	HTTP *http.Client
	// Now is the clock the signature is dated with. Tests freeze it so a signature is reproducible.
	Now func() time.Time

	cfg Config // Endpoint is the normalized base URL (scheme://host) once New returns
}

// defaultHTTP is shared: one transport per process pools connections to the same endpoint.
var defaultHTTP = &http.Client{Timeout: defaultTimeout}

var (
	// ErrObjectTooLarge is what Get reports for an object over its maxBytes; retention and restore code paths
	// branch on it to tell "refusing to read" from "the store is broken".
	ErrObjectTooLarge = errors.New("object is larger than the read limit")
	// ErrNotFound is what Stat reports for an absent key, because "how big is it" and "it is not there" are
	// different answers and a caller must not tell them apart by string matching.
	ErrNotFound = errors.New("object not found")
)

// New validates cfg and returns a client for its bucket.
func New(cfg Config) (*Client, error) {
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.Region = strings.TrimSpace(cfg.Region)
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	cfg.AccessKey = strings.TrimSpace(cfg.AccessKey)
	if cfg.Bucket == "" {
		return nil, errors.New("objectstore: bucket is required")
	}
	// Path-style addressing puts the bucket in the URL next to the key, so a separator inside the name would
	// silently address a different path than the one configured.
	if strings.ContainsRune(cfg.Bucket, '/') {
		return nil, fmt.Errorf("objectstore: bucket %q is not a valid name", cfg.Bucket)
	}
	if cfg.AccessKey == "" {
		return nil, errors.New("objectstore: access key is required")
	}
	// An all-whitespace secret is a half-filled form, not a credential. The value itself is never trimmed: it is
	// opaque, and reshaping it would change what we sign with while still looking like what was configured.
	if strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, errors.New("objectstore: secret key is required")
	}
	if cfg.Region == "" {
		return nil, errors.New("objectstore: region is required: it is part of the credential scope")
	}
	raw := cfg.Endpoint
	if raw == "" {
		raw = "https://s3." + cfg.Region + ".amazonaws.com"
	}
	endpoint, err := normalizeEndpoint(raw)
	if err != nil {
		return nil, err
	}
	cfg.Endpoint = endpoint
	return &Client{cfg: cfg}, nil
}

// Bucket is the bucket every call is scoped to.
func (c *Client) Bucket() string { return c.cfg.Bucket }

// Prefix is the normalized prefix Key adds to every name. A caller that stores a full object URI and has to read
// it back needs this: Put, Get, Stat and Delete apply the prefix themselves, so the name handed to them is the one
// without it, and passing the stored URI straight through would address a different object than the one written.
func (c *Client) Prefix() string { return strings.Trim(strings.TrimSpace(c.cfg.Prefix), "/") }

// Key is the object name k is stored under: the configured prefix joined to k by exactly one slash. An empty
// prefix and a leading slash on k are both tolerated, since a caller building a name from a resource id plus a
// filename should not have to know which combination produced the string it holds.
func (c *Client) Key(k string) string {
	k = strings.TrimLeft(k, "/")
	p := strings.Trim(strings.TrimSpace(c.cfg.Prefix), "/")
	switch {
	case p == "":
		return k
	case k == "":
		return p
	}
	return p + "/" + k
}

// Put uploads body, which must be exactly size bytes and hash to sha256Hex. The reader is streamed: a dump is
// never buffered, which is why the hash arrives pre-computed rather than being taken here.
func (c *Client) Put(ctx context.Context, k string, r io.Reader, size int64, sha256Hex string) error {
	if size < 0 {
		return fmt.Errorf("objectstore: put %q: size must not be negative", k)
	}
	if !isHexSHA256(sha256Hex) {
		// A wrong hash reaches the server as a signature mismatch, which points at our code instead of at the
		// caller that computed it.
		return fmt.Errorf("objectstore: put %q: payload hash must be %d hex characters", k, sha256HexLen)
	}
	resp, err := c.do(ctx, http.MethodPut, k, r, size, sha256Hex)
	if err != nil {
		return err
	}
	// The reply is only an ETag this client keeps no use for, and a close failure cannot undo an upload the
	// status line already accepted.
	_ = resp.Body.Close()
	return nil
}

// Get returns an object's bytes, refusing to read more than maxBytes.
func (c *Client) Get(ctx context.Context, k string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("objectstore: get %q: maxBytes must be positive", k)
	}
	resp, err := c.do(ctx, http.MethodGet, k, nil, 0, emptyPayloadHash)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// A declared size over the limit is refused before the body is transferred: an operator asking for a dump
	// they cannot hold should not pay for downloading it first.
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("objectstore: get %q: %w: object is %d bytes, limit %d", k, ErrObjectTooLarge, resp.ContentLength, maxBytes)
	}
	// One byte past the limit, so a body longer than the limit is detected instead of being read in full.
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("objectstore: get %q: %w", k, err)
	}
	if int64(len(b)) > maxBytes {
		return nil, fmt.Errorf("objectstore: get %q: %w: object is larger than %d bytes", k, ErrObjectTooLarge, maxBytes)
	}
	return b, nil
}

// Stat returns an object's size and ETag.
func (c *Client) Stat(ctx context.Context, k string) (size int64, etag string, err error) {
	resp, err := c.do(ctx, http.MethodHead, k, nil, 0, emptyPayloadHash, http.StatusNotFound)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, "", fmt.Errorf("objectstore: stat %q: %w", k, ErrNotFound)
	}
	if resp.ContentLength < 0 {
		// A HEAD that declares no length cannot be answered with a size; a guessed zero would propagate into
		// retention accounting.
		return 0, "", fmt.Errorf("objectstore: stat %q: %s sent no Content-Length", k, c.cfg.Endpoint)
	}
	return resp.ContentLength, resp.Header.Get("ETag"), nil
}

// Delete removes an object; an absent key is not an error, so a retried delete after a lost response cannot
// leave the caller stuck on a failure it cannot undo.
func (c *Client) Delete(ctx context.Context, k string) error {
	resp, err := c.do(ctx, http.MethodDelete, k, nil, 0, emptyPayloadHash, http.StatusNotFound)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// do signs and sends one request, returning only a response whose status the caller accepts: 2xx, or one of
// allow. Every error names the operation and carries the status plus a truncated body, because S3 puts the
// reason for the failure in that XML and the operator reading a log needs it.
func (c *Client) do(ctx context.Context, method, k string, body io.Reader, size int64, payloadHash string, allow ...int) (*http.Response, error) {
	path, err := c.objectPath(k)
	if err != nil {
		return nil, fmt.Errorf("objectstore: %s %q: %w", method, k, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.Endpoint+path, body)
	if err != nil {
		return nil, fmt.Errorf("objectstore: %s %q: %w", method, k, err)
	}
	req.ContentLength = size
	c.sign(req, payloadHash)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		// Transport and context errors quote the URL; New refused an endpoint carrying credentials, so no key
		// can reach this message.
		return nil, fmt.Errorf("objectstore: %s %q: %w", method, k, err)
	}
	for _, code := range allow {
		if resp.StatusCode == code {
			return resp, nil
		}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit+1))
	return nil, fmt.Errorf("objectstore: %s %q: %s: %s", method, k, resp.Status, truncateBody(b))
}

// sign dates the request and adds its Authorization header. The signed set is the three headers this client
// sets, lowercase and sorted: the server rebuilds the canonical request from exactly that list, so a fourth
// header has to be added to signedHeaders as well or the signature will not match.
func (c *Client) sign(req *http.Request, payloadHash string) {
	now := c.now().UTC()
	amzDate := now.Format(amzDateFormat)
	scope := strings.Join([]string{amzDate[:8], c.cfg.Region, service, requestTerm}, "/")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	host := req.URL.Host
	if req.Host != "" {
		host = req.Host
	}
	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	// EscapedPath, not Path: the canonical URI has to be the bytes that go on the wire, or the signature the
	// server computes will not match the one we did.
	canonical := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		// Every request this client makes is query-free, so the canonical query string line is empty.
		"",
		// The header block ends with a newline of its own; the join adds the blank line after it.
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{algorithm, amzDate, scope, hexSHA256([]byte(canonical))}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(c.cfg.SecretKey, amzDate[:8], c.cfg.Region), []byte(stringToSign)))
	req.Header.Set("Authorization", algorithm+" Credential="+c.cfg.AccessKey+"/"+scope+
		", SignedHeaders="+signedHeaders+", Signature="+signature)
}

// signingKey is the four-step derivation the spec prescribes: the secret never signs anything directly, so it
// never leaves the process even in a debugging dump of an intermediate key.
func signingKey(secret, dateStamp, region string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte(service))
	return hmacSHA256(k, []byte(requestTerm))
}

func (c *Client) objectPath(k string) (string, error) {
	key := c.Key(k)
	if key == "" {
		return "", errors.New("empty object key")
	}
	segments := strings.Split(key, "/")
	for i, s := range segments {
		segments[i] = encodePathSegment(s)
	}
	// Each segment is encoded on its own so the separators survive: S3 keys are namespaced by those slashes.
	return "/" + c.cfg.Bucket + "/" + strings.Join(segments, "/"), nil
}

// normalizeEndpoint takes "host", "http://host" or "https://host", with or without a trailing slash, and returns
// the base URL to dial. The scheme defaults to https because every endpoint this panel points at is either a
// cloud or a TLS-terminating reverse proxy; a LAN MinIO on plain http states its scheme.
func normalizeEndpoint(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	// Both checks run before anything echoes the value: an @ here carries credentials, and a query string means
	// the operator pasted a presigned request, whose signature must not reach a log through a parse error.
	if strings.ContainsRune(raw, '@') {
		return "", errors.New("objectstore: endpoint must not carry credentials")
	}
	if strings.ContainsAny(raw, "?#") {
		return "", errors.New("objectstore: endpoint must be a host only, not a presigned URL")
	}
	u, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil {
		return "", fmt.Errorf("objectstore: invalid endpoint: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return "", errors.New("objectstore: endpoint scheme must be http or https")
	}
	if u.Host == "" {
		return "", errors.New("objectstore: endpoint must name a host")
	}
	if strings.Trim(u.Path, "/") != "" {
		return "", errors.New("objectstore: endpoint must be a host only: path-style addressing puts the bucket after it")
	}
	return u.Scheme + "://" + u.Host, nil
}

func isHexSHA256(s string) bool {
	return len(s) == sha256HexLen && strings.Trim(s, "0123456789abcdefABCDEF") == ""
}

// encodePathSegment is AWS's URI-encoding rules for one path segment: unreserved characters as themselves,
// every other byte as an uppercase percent escape. Applied per segment rather than to the whole key, because a
// key's own slashes must not be encoded into %2F: S3 reads them as the namespace, and the canonical URI has to
// name the same object as the request line does.
func encodePathSegment(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		default:
			b.WriteByte('%')
			b.WriteByte(upperhex[ch>>4])
			b.WriteByte(upperhex[ch&0x0f])
		}
	}
	return b.String()
}

// truncateBody keeps the useful part of an error reply and marks the cut, so a truncated XML document still
// reads as truncated.
func truncateBody(b []byte) string {
	b = bytes.TrimSpace(b)
	if len(b) > errorBodyLimit {
		return string(b[:errorBodyLimit]) + "..."
	}
	return string(b)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
