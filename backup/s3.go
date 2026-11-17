package backup

import (
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // Content-MD5 is the integrity header S3 asks for (object lock), not a security hash
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultTimeout bounds one upload, the body included.
	DefaultTimeout = 5 * time.Minute
	// maxErrorBody is all that is read of an error response.
	maxErrorBody = 4 << 10
	amzDate      = "20060102T150405Z"
)

// s3Code is what an S3 error code may look like; anything else is dropped,
// so a hostile server cannot put text of its own into an error.
var s3Code = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

// StatusError is a response of the bucket's endpoint other than 2xx. It
// holds the status and the S3 error code only: never the rest of the body
// and no header (R-BAK-3).
type StatusError struct {
	Status int
	// Code is the S3 error code ("AccessDenied"), empty when the body had none.
	Code string
}

func (e *StatusError) Error() string {
	if e.Code == "" {
		return "backup: put: status " + strconv.Itoa(e.Status)
	}
	return "backup: put: status " + strconv.Itoa(e.Status) + ": " + e.Code
}

// S3Config is the bucket a Client puts into. Endpoint is an https URL with
// a host and no path; the bucket is addressed path-style, as
// https://host/bucket/key.
type S3Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
}

// Client puts objects into an S3-compatible bucket with AWS Signature
// Version 4, with no SDK (ADR-0095). It never lists, reads or deletes.
type Client struct {
	cfg  S3Config
	base *url.URL
	hc   *http.Client
	now  func() time.Time
}

// S3Option configures a Client.
type S3Option func(*Client)

// WithHTTPClient replaces the HTTP client (tests). Redirects are never
// followed, whatever the client says.
func WithHTTPClient(hc *http.Client) S3Option { return func(c *Client) { c.hc = hc } }

// WithClock replaces the clock the request is signed with (tests).
func WithClock(now func() time.Time) S3Option { return func(c *Client) { c.now = now } }

// NewClient checks cfg and makes a Client. The endpoint must be https: a
// credential and an archive are not sent in the clear.
func NewClient(cfg S3Config, opts ...S3Option) (*Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	switch {
	case err != nil:
		return nil, errors.New("backup: the endpoint is not a URL")
	case u.Scheme != "https" || u.Hostname() == "":
		return nil, errors.New("backup: the endpoint must be an https URL with a host")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/"):
		return nil, errors.New("backup: the endpoint must be a bare host: no credentials, path or query")
	case cfg.Region == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "":
		return nil, errors.New("backup: the region, the access key id and the secret key are required")
	case cfg.Bucket == "" || strings.ContainsAny(cfg.Bucket, "/?#"):
		return nil, errors.New("backup: the bucket is not a bucket name")
	}
	c := &Client{cfg: cfg, base: &url.URL{Scheme: "https", Host: u.Host}, now: time.Now,
		hc: &http.Client{Timeout: DefaultTimeout}}
	for _, o := range opts {
		o(c)
	}
	// A copy, so the caller's client keeps its own redirect rule.
	hc := *c.hc
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.hc = &hc
	return c, nil
}

// Put stores size bytes of body under key in one PutObject request. The body
// is read twice: once for its SHA-256 and MD5, once to send. A response other
// than 2xx is a *StatusError.
func (c *Client) Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error {
	if key == "" || strings.HasPrefix(key, "/") {
		return errors.New("backup: put: the key must be a non-empty relative name")
	}
	sum, md5sum, err := digests(body, size)
	if err != nil {
		return err
	}
	u := *c.base
	u.Path = "/" + c.cfg.Bucket + "/" + key
	path := canonicalPath(c.cfg.Bucket, key)
	u.RawPath = path
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), io.NopCloser(body))
	if err != nil {
		return fmt.Errorf("backup: put: %w", err)
	}
	req.ContentLength = size
	now := c.now().UTC()
	headers := map[string]string{
		"content-length":       strconv.FormatInt(size, 10),
		"content-md5":          md5sum,
		"content-type":         "application/octet-stream",
		"host":                 u.Host,
		"x-amz-content-sha256": sum,
		"x-amz-date":           now.Format(amzDate),
	}
	for k, v := range headers {
		if k != "host" && k != "content-length" {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("Authorization", sign(c.cfg, http.MethodPut, path, "", headers, sum, now))
	resp, err := c.hc.Do(req)
	if err != nil {
		// The URL is not repeated: it holds the bucket and the key.
		return fmt.Errorf("backup: put: %w", unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 == 2 {
		return nil
	}
	return statusError(resp)
}

// unwrapURLError drops the request URL from a transport error.
func unwrapURLError(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err
	}
	return err
}

// digests reads size bytes of body from its start and returns the hex
// SHA-256 and the base64 MD5, leaving body at its start.
func digests(body io.ReadSeeker, size int64) (sum, md5sum string, err error) {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", "", fmt.Errorf("backup: put: %w", err)
	}
	s, m := sha256.New(), md5.New() //nolint:gosec // see the import
	n, err := io.Copy(io.MultiWriter(s, m), body)
	if err != nil {
		return "", "", fmt.Errorf("backup: put: read body: %w", err)
	}
	if n != size {
		return "", "", fmt.Errorf("backup: put: the body is %d bytes, not the %d announced", n, size)
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", "", fmt.Errorf("backup: put: %w", err)
	}
	return hex.EncodeToString(s.Sum(nil)), base64.StdEncoding.EncodeToString(m.Sum(nil)), nil
}

// statusError reads at most maxErrorBody of an error response and keeps the
// S3 error code from it.
func statusError(resp *http.Response) error {
	e := &StatusError{Status: resp.StatusCode}
	var doc struct {
		Code string `xml:"Code"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if xml.Unmarshal(data, &doc) == nil && s3Code.MatchString(doc.Code) {
		e.Code = doc.Code
	}
	return e
}

// canonicalPath is the URI of an object, path-style: every segment of the
// bucket and the key escaped by the SigV4 rules, the slashes between them
// kept. S3 does not escape twice.
func canonicalPath(bucket, key string) string {
	segs := append([]string{bucket}, strings.Split(key, "/")...)
	for i, s := range segs {
		segs[i] = uriEscape(s)
	}
	return "/" + strings.Join(segs, "/")
}

// uriEscape percent-encodes everything but A-Z a-z 0-9 - _ . ~ with
// upper-case hex, as SigV4 asks.
func uriEscape(s string) string {
	const hexUpper = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		ch := s[i]
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' ||
			ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			b.WriteByte(ch)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexUpper[ch>>4])
		b.WriteByte(hexUpper[ch&15])
	}
	return b.String()
}

// sign returns the Authorization header value of AWS Signature Version 4
// for the S3 service. headers are the lower-case names and the values of
// the headers to sign; path is the escaped URI and query the canonical
// query string.
func sign(cfg S3Config, method, path, query string, headers map[string]string, payloadHash string, now time.Time) string {
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	slices.Sort(names)
	var canon strings.Builder
	for _, k := range names {
		canon.WriteString(k + ":" + strings.TrimSpace(headers[k]) + "\n")
	}
	signed := strings.Join(names, ";")
	request := strings.Join([]string{method, path, query, canon.String(), signed, payloadHash}, "\n")

	date := now.UTC().Format("20060102")
	scope := date + "/" + cfg.Region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + now.UTC().Format(amzDate) + "\n" + scope + "\n" + hexSHA256(request)

	k := hmacSHA256([]byte("AWS4"+cfg.SecretAccessKey), date)
	k = hmacSHA256(k, cfg.Region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(k, toSign))
	return "AWS4-HMAC-SHA256 Credential=" + cfg.AccessKeyID + "/" + scope +
		", SignedHeaders=" + signed + ", Signature=" + signature
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
