// Package objectstore is the archive destination behind retention.ObjectStore.
//
// S3-compatible rather than S3: the archive of a customer's evidence is the thing they
// are most likely to want somewhere specific — their own bucket, their own region, their
// own provider, an appliance in their own building — and a client that speaks to one
// vendor makes that a rebuild rather than a configuration.
//
// Written against the protocol rather than pulled from a vendor SDK. The obvious client
// brings thirteen transitive dependencies, and the repository's dependency allowlist
// exists to make that a decision rather than a reflex: this package performs one PUT and
// one GET, both off the hot path, and Signature Version 4 over a known payload is about
// a hundred lines of standard-library hashing. A supply chain is a control surface too,
// and thirteen packages is a poor exchange for two verbs.
package objectstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// S3 stores archives in an S3-compatible bucket.
type S3 struct {
	cfg    Config
	scheme string
	client *http.Client
}

// Config is what it takes to reach a bucket.
//
// The credentials are read from the environment by the caller and never written down
// here: spec section 35 keeps secrets out of the source, out of logs, out of evidence
// and out of API responses, and an object-store key that can read a customer's evidence
// archive is exactly the kind it is written about.
type Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseTLS    bool
}

// New prepares a client and proves the bucket is reachable.
//
// It does not create the bucket. Where a customer's evidence is archived, and under what
// lifecycle and retention-lock settings, is their decision and their provider's console;
// a process that creates its own bucket creates one with default settings, and defaults
// are how an archive ends up deletable.
func New(ctx context.Context, cfg Config) (*S3, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, fmt.Errorf("an object store needs an endpoint and a bucket")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	scheme := "http"
	if cfg.UseTLS {
		scheme = "https"
	}
	s := &S3{cfg: cfg, scheme: scheme, client: &http.Client{Timeout: 60 * time.Second}}

	// HEAD on the bucket, so a misconfigured endpoint or a wrong key is a startup
	// error rather than a failed archive discovered at retention time.
	req, err := s.request(ctx, http.MethodHead, "", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach the bucket: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bucket %s is not usable: %s", cfg.Bucket, resp.Status)
	}
	return s, nil
}

// Put writes an archive.
//
// The whole body at a known length, rather than a stream of unknown size: the caller has
// already hashed these exact bytes, and an upload that could be truncated by a short
// read would produce an archive whose manifest describes something else.
func (s *S3) Put(ctx context.Context, key string, body []byte) error {
	req, err := s.request(ctx, http.MethodPut, key, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("put %s: %s: %s", key, resp.Status, snippet(resp.Body))
	}
	return nil
}

// Get reads an archive back whole.
func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	req, err := s.request(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// A missing archive is reported as the failure it is. An empty body returned
		// as success would restore zero events and verify as a truncated archive,
		// which points an operator at tampering when the object is simply gone.
		return nil, fmt.Errorf("get %s: %s: %s", key, resp.Status, snippet(resp.Body))
	}
	return io.ReadAll(resp.Body)
}

// request builds and signs one request.
//
// Path-style addressing (host/bucket/key) rather than virtual-hosted. Every S3-compatible
// implementation serves it, and a bucket name reached as a subdomain needs DNS the
// customer may not control.
func (s *S3) request(ctx context.Context, method, key string, body []byte) (*http.Request, error) {
	path := "/" + s.cfg.Bucket
	if key != "" {
		path += "/" + key
	}
	target := &url.URL{Scheme: s.scheme, Host: s.cfg.Endpoint, Path: path}

	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))
	s.sign(req, body, time.Now().UTC())
	return req, nil
}

// sign is AWS Signature Version 4 over the exact bytes being sent.
//
// The payload hash is the point, not a formality: it goes into the signature, so a body
// altered anywhere between here and the bucket produces a request the server refuses. It
// is never UNSIGNED-PAYLOAD here — the caller is uploading evidence whose integrity is
// the whole product.
func (s *S3) sign(req *http.Request, body []byte, at time.Time) {
	stamp := at.Format("20060102T150405Z")
	day := at.Format("20060102")
	payloadHash := hex.EncodeToString(sha256sum(body))

	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	// Signed headers, sorted, as the canonical request requires.
	signed := "host;x-amz-content-sha256;x-amz-date"
	canonical := strings.Join([]string{
		req.Method,
		escapePath(req.URL.Path),
		"", // no query
		"host:" + req.URL.Host + "\n" +
			"x-amz-content-sha256:" + payloadHash + "\n" +
			"x-amz-date:" + stamp + "\n",
		signed,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{day, s.cfg.Region, "s3", "aws4_request"}, "/")
	toSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		stamp,
		scope,
		hex.EncodeToString(sha256sum([]byte(canonical))),
	}, "\n")

	key := hmacSHA256([]byte("AWS4"+s.cfg.SecretKey), day)
	key = hmacSHA256(key, s.cfg.Region)
	key = hmacSHA256(key, "s3")
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, toSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.cfg.AccessKey, scope, signed, signature))
}

// escapePath encodes a path the way SigV4 canonicalization requires: every segment
// escaped, and the separators left alone.
func escapePath(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(url.PathEscape(p), "+", "%20")
	}
	return strings.Join(parts, "/")
}

func sha256sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// snippet returns enough of an error body to name the cause without pasting a page of
// XML into a log line.
func snippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}
