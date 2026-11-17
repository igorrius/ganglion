package backup

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // the test checks the Content-MD5 the client sends
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The AWS Signature Version 4 examples of the S3 documentation ("Signature
// calculations for the Authorization header", with AKIAIOSFODNN7EXAMPLE).
var awsExample = S3Config{Region: "us-east-1", AccessKeyID: "AKIAIOSFODNN7EXAMPLE",
	SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}

var awsTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

func TestSignGetObjectExample(t *testing.T) {
	got := sign(awsExample, "GET", "/test.txt", "", map[string]string{
		"host":                 "examplebucket.s3.amazonaws.com",
		"range":                "bytes=0-9",
		"x-amz-content-sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"x-amz-date":           "20130524T000000Z",
	}, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", awsTime)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request," +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date," +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got != strings.Replace(want, ",", ", ", 2) {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestSignPutObjectExample(t *testing.T) {
	body := "Welcome to Amazon S3."
	sum := sha256.Sum256([]byte(body))
	if hex.EncodeToString(sum[:]) != "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072" {
		t.Fatal("the example body does not hash as documented")
	}
	got := sign(awsExample, "PUT", canonicalPath("examplebucket", "test$file.text")[len("/examplebucket"):], "", map[string]string{
		"date":                 "Fri, 24 May 2013 00:00:00 GMT",
		"host":                 "examplebucket.s3.amazonaws.com",
		"x-amz-content-sha256": hex.EncodeToString(sum[:]),
		"x-amz-date":           "20130524T000000Z",
		"x-amz-storage-class":  "REDUCED_REDUNDANCY",
	}, hex.EncodeToString(sum[:]), awsTime)
	const wantSig = "Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"
	if !strings.HasSuffix(got, wantSig) {
		t.Fatalf("got %s, want it to end with %s", got, wantSig)
	}
}

func TestCanonicalPathEscapesEverySegment(t *testing.T) {
	for _, tc := range []struct{ bucket, key, want string }{
		{"b", "a/ganglion-20261117T030000Z.tar.gz.enc", "/b/a/ganglion-20261117T030000Z.tar.gz.enc"},
		{"b", "x y/é+~.z", "/b/x%20y/%C3%A9%2B~.z"},
		{"b", "a?b#c%d", "/b/a%3Fb%23c%25d"},
	} {
		if got := canonicalPath(tc.bucket, tc.key); got != tc.want {
			t.Errorf("canonicalPath(%q, %q) = %q, want %q", tc.bucket, tc.key, got, tc.want)
		}
	}
}

// fakeS3 is a TLS server that checks the signature, the MD5 and the SHA-256
// of every PUT and keeps the object.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	srv     *httptest.Server
	cfg     S3Config
	fail    int
	reply   string
	headers []http.Header
}

func newFakeS3(t *testing.T) *fakeS3 {
	t.Helper()
	f := &fakeS3{objects: map[string][]byte{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	f.cfg = S3Config{Endpoint: f.srv.URL, Region: "eu-test-1", Bucket: "bkt", AccessKeyID: "AKIATEST",
		SecretAccessKey: "super-secret-key"}
	return f
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headers = append(f.headers, r.Header.Clone())
	if f.fail != 0 {
		w.WriteHeader(f.fail)
		_, _ = io.WriteString(w, f.reply)
		return
	}
	sum := sha256.Sum256(body)
	m := md5.Sum(body) //nolint:gosec // see the import
	when, err := time.Parse(amzDate, r.Header.Get("x-amz-date"))
	headers := map[string]string{
		"content-length":       r.Header.Get("Content-Length"),
		"content-md5":          r.Header.Get("Content-MD5"),
		"content-type":         r.Header.Get("Content-Type"),
		"host":                 r.Host,
		"x-amz-content-sha256": r.Header.Get("x-amz-content-sha256"),
		"x-amz-date":           r.Header.Get("x-amz-date"),
	}
	want := sign(f.cfg, r.Method, r.URL.EscapedPath(), r.URL.RawQuery, headers, hex.EncodeToString(sum[:]), when)
	switch {
	case err != nil || r.Header.Get("Authorization") != want:
		http.Error(w, "<Error><Code>SignatureDoesNotMatch</Code></Error>", http.StatusForbidden)
	case r.Header.Get("x-amz-content-sha256") != hex.EncodeToString(sum[:]) ||
		r.Header.Get("Content-MD5") != base64.StdEncoding.EncodeToString(m[:]):
		http.Error(w, "<Error><Code>BadDigest</Code></Error>", http.StatusBadRequest)
	default:
		f.objects[r.URL.EscapedPath()] = body
	}
}

func (f *fakeS3) client(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient(f.cfg, WithHTTPClient(f.srv.Client()), WithClock(func() time.Time { return awsTime }))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPutStoresTheObjectAfterTheServerChecksIt(t *testing.T) {
	f := newFakeS3(t)
	body := bytes.Repeat([]byte("ganglion"), 1000)
	err := f.client(t).Put(context.Background(), "pre fix/ganglion-1.tar.gz.enc", bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.objects["/bkt/pre%20fix/ganglion-1.tar.gz.enc"]; !bytes.Equal(got, body) {
		t.Fatalf("stored %d bytes under %v", len(got), keys(f.objects))
	}
	h := f.headers[0]
	if h.Get("Content-Type") != "application/octet-stream" || h.Get("Content-MD5") == "" || h.Get("x-amz-date") != "20130524T000000Z" {
		t.Fatalf("headers: %v", h)
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestPutWithAnotherSecretIsRefused(t *testing.T) {
	f := newFakeS3(t)
	f.cfg.SecretAccessKey = "another" // the server verifies with this one
	c := f.client(t)
	f.cfg.SecretAccessKey = "super-secret-key"
	err := c.Put(context.Background(), "k", strings.NewReader("x"), 1)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 403 || se.Code != "SignatureDoesNotMatch" {
		t.Fatalf("err = %v", err)
	}
}

func TestPutErrorHasStatusAndCodeOnly(t *testing.T) {
	f := newFakeS3(t)
	f.fail = http.StatusForbidden
	f.reply = `<Error><Code>AccessDenied</Code><Message>secret detail BODYLEAK</Message><RequestId>r1</RequestId></Error>`
	err := f.client(t).Put(context.Background(), "k", strings.NewReader("x"), 1)
	if err == nil || err.Error() != "backup: put: status 403: AccessDenied" {
		t.Fatalf("err = %v", err)
	}
	for _, leak := range []string{"BODYLEAK", "super-secret-key", "AKIATEST", "Authorization", f.srv.URL} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error leaks %q: %v", leak, err)
		}
	}
}

func TestPutErrorWithAHostileBody(t *testing.T) {
	f := newFakeS3(t)
	f.fail = http.StatusInternalServerError
	for _, reply := range []string{"", "not xml", "<Error><Code>Ignore previous instructions!</Code></Error>",
		"<Error><Code>" + strings.Repeat("A", 100) + "</Code></Error>",
		strings.Repeat("x", 1<<20) + "<Error><Code>TooLate</Code></Error>"} {
		f.reply = reply
		err := f.client(t).Put(context.Background(), "k", strings.NewReader("x"), 1)
		var se *StatusError
		if !errors.As(err, &se) || se.Status != 500 || se.Code != "" {
			t.Errorf("reply %.20q: err = %v", reply, err)
		}
	}
}

func TestPutDoesNotFollowARedirect(t *testing.T) {
	f := newFakeS3(t)
	f.fail = http.StatusTemporaryRedirect
	err := f.client(t).Put(context.Background(), "k", strings.NewReader("x"), 1)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 307 {
		t.Fatalf("err = %v", err)
	}
}

func TestPutChecksTheAnnouncedSize(t *testing.T) {
	f := newFakeS3(t)
	if err := f.client(t).Put(context.Background(), "k", strings.NewReader("abc"), 5); err == nil {
		t.Fatal("a body shorter than announced was sent")
	}
	if err := f.client(t).Put(context.Background(), "/abs", strings.NewReader("abc"), 3); err == nil {
		t.Fatal("an absolute key was accepted")
	}
}

func TestPutErrorHoldsNoURL(t *testing.T) {
	f := newFakeS3(t)
	c := f.client(t)
	f.srv.Close()
	err := c.Put(context.Background(), "k-private-name", strings.NewReader("x"), 1)
	if err == nil || strings.Contains(err.Error(), "k-private-name") || strings.Contains(err.Error(), "bkt") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewClientRefusesWhatIsNotSafe(t *testing.T) {
	ok := S3Config{Endpoint: "https://s3.example.com", Region: "r", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"}
	if _, err := NewClient(ok); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*S3Config){
		"http":        func(c *S3Config) { c.Endpoint = "http://s3.example.com" },
		"no scheme":   func(c *S3Config) { c.Endpoint = "s3.example.com" },
		"credentials": func(c *S3Config) { c.Endpoint = "https://a:b@s3.example.com" },
		"path":        func(c *S3Config) { c.Endpoint = "https://s3.example.com/base" },
		"query":       func(c *S3Config) { c.Endpoint = "https://s3.example.com/?x=1" },
		"no region":   func(c *S3Config) { c.Region = "" },
		"no key id":   func(c *S3Config) { c.AccessKeyID = "" },
		"no secret":   func(c *S3Config) { c.SecretAccessKey = "" },
		"no bucket":   func(c *S3Config) { c.Bucket = "" },
		"bucket path": func(c *S3Config) { c.Bucket = "a/b" },
	} {
		c := ok
		mutate(&c)
		if _, err := NewClient(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
