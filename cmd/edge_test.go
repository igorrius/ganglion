package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/edgejoin"
	"github.com/igorrius/ganglion/secrets"
	"sync"
)

// fakeNode answers POST /edge/join the way the node does: the code "good"
// gets a JWT for the key, any other a refusal in the node's words.
func fakeNode(t *testing.T) *httptest.Server {
	t.Helper()
	chain, err := natsbus.LoadChain(context.Background(), &mapStore{m: map[string]string{}}, func() bool { return false }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	opPub := chain.OperatorPublicKey()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q edgejoin.JoinRequest
		_ = json.NewDecoder(r.Body).Decode(&q)
		if q.Code != "good" {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "the code is not valid"})
			return
		}
		tok, err := chain.IssueUser("home", q.PublicKey, natsbus.UserPermissions{}, time.Hour)
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(edgejoin.Answer{Name: "home", JWT: tok, Operator: opPub, Expires: time.Now().Add(time.Hour)})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func joinCmd(t *testing.T, in string, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := newRootIO(strings.NewReader(in), &out, &errOut, nil).Run(context.Background(), append([]string{"ganglion", "edge", "join"}, args...))
	return out.String() + errOut.String(), err
}

func TestEdgeJoinWritesPrivateCredentialsAndPrintsTheNextStep(t *testing.T) {
	srv := fakeNode(t)
	dir := filepath.Join(t.TempDir(), "data")
	db := filepath.Join(dir, "node.db")
	out, err := joinCmd(t, "", "--db", db, srv.URL, "good")
	if err != nil {
		t.Fatalf("join: %v\n%s", err, out)
	}
	creds := filepath.Join(dir, "edge.creds")
	st, err := os.Stat(creds)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("creds: %v, %v", st, err)
	}
	if d, _ := os.Stat(dir); d.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v, want 0700", d.Mode().Perm())
	}
	raw, _ := os.ReadFile(creds)
	if _, err := natsbus.UserKeyFromCreds(raw); err != nil {
		t.Fatalf("creds do not parse: %v", err)
	}
	seed := strings.SplitAfter(string(raw), "SEED-----\n")[1][:58]
	tok := strings.SplitAfter(string(raw), "JWT-----\n")[1]
	tok = tok[:strings.Index(tok, "\n")]
	for _, secret := range []string{seed, tok, "good"} {
		if strings.Contains(out, secret) {
			t.Fatalf("the output shows a secret:\n%s", out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "edge.operator")); err != nil {
		t.Fatalf("operator file: %v", err)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	for _, want := range []string{`"home"`, "name: home", "url: ws://" + host + "/bus", "ganglion run --roles edge --db " + db} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestEdgeJoinRefusesToOverwriteWithoutForce(t *testing.T) {
	srv := fakeNode(t)
	db := filepath.Join(t.TempDir(), "node.db")
	creds := filepath.Join(filepath.Dir(db), "edge.creds")
	if err := os.WriteFile(creds, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := joinCmd(t, "", "--db", db, srv.URL, "good"); err == nil {
		t.Fatalf("overwrote without --force:\n%s", out)
	}
	if got, _ := os.ReadFile(creds); string(got) != "old" {
		t.Fatal("the file changed")
	}
	if out, err := joinCmd(t, "", "--db", db, "--force", srv.URL, "good"); err != nil {
		t.Fatalf("--force: %v\n%s", err, out)
	}
	got, _ := os.ReadFile(creds)
	if _, err := natsbus.UserKeyFromCreds(got); err != nil {
		t.Fatalf("--force left %q", got)
	}
	if st, _ := os.Stat(creds); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
}

func TestEdgeJoinShowsTheNodesRefusalAndWritesNothing(t *testing.T) {
	srv := fakeNode(t)
	db := filepath.Join(t.TempDir(), "node.db")
	out, err := joinCmd(t, "", "--db", db, srv.URL, "bad-code-123")
	if err == nil || !strings.Contains(err.Error(), "the code is not valid") {
		t.Fatalf("error = %v\n%s", err, out)
	}
	if strings.Contains(err.Error(), "bad-code-123") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("the error repeats the code or the address: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(db), "edge.creds")); err == nil {
		t.Fatal("a refused join wrote credentials")
	}
}

func TestEdgeJoinReadsTheCodeFromStandardInput(t *testing.T) {
	srv := fakeNode(t)
	db := filepath.Join(t.TempDir(), "node.db")
	if out, err := joinCmd(t, "good\n", "--db", db, srv.URL, "-"); err != nil {
		t.Fatalf("join: %v\n%s", err, out)
	}
}

func TestEdgeJoinNeedsHTTPS(t *testing.T) {
	db := filepath.Join(t.TempDir(), "node.db")
	if _, err := joinCmd(t, "", "--db", db, "http://example.com", "good"); err == nil {
		t.Fatal("plain http to a remote host was accepted")
	}
}

// mapStore is a secret store in memory for the test chain.
type mapStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *mapStore) Get(_ context.Context, sc secrets.Scope, n string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[string(sc)+"/"+n], nil
}

func (s *mapStore) Set(_ context.Context, sc secrets.Scope, n, v, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[string(sc)+"/"+n] = v
	return nil
}
