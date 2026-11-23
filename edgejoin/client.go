package edgejoin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// Credentials are what an edge keeps after a join: its .creds text (the user
// JWT and the seed), the name the owner gave it, the operator's public key
// and the bus address.
type Credentials struct {
	Name     string
	Creds    []byte
	Operator string
	BusURL   string
	Expires  time.Time
}

// maxAnswer bounds the core's answer.
const maxAnswer = 64 << 10

// Endpoint checks the address the owner gave and returns it as the base of
// the routes: https, or http for a loopback host (a test node), no user
// information, no query.
func Endpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("edgejoin: the address must be the node's public address, such as https://example.com")
	}
	loop := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loop) {
		return nil, errors.New("edgejoin: the address must start with https://")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

// BusURLOf is the bus address on the node's public address:
// wss://<host>/bus (R-EDGE-4).
func BusURLOf(base *url.URL) string {
	scheme := "wss"
	if base.Scheme == "http" {
		scheme = "ws"
	}
	return scheme + "://" + base.Host + base.Path + "/bus"
}

// Join makes the edge's key, which never leaves this process except as the
// returned Credentials, and trades the code for the user JWT. A refusal is
// an error with the node's words.
func Join(ctx context.Context, hc *http.Client, node, code string) (Credentials, error) {
	base, err := Endpoint(node)
	if err != nil {
		return Credentials{}, err
	}
	kp, err := nkeys.CreateUser()
	if err != nil {
		return Credentials{}, fmt.Errorf("edgejoin: make the key: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return Credentials{}, fmt.Errorf("edgejoin: make the key: %w", err)
	}
	ans, err := post(ctx, hc, base, JoinPath, JoinRequest{Code: code, PublicKey: pub})
	if err != nil {
		return Credentials{}, err
	}
	return finish(kp, base, ans)
}

// Renew asks for a new JWT with the permissions of now, signing the request
// with the edge's key from its creds file. The seed is read from creds and
// not kept.
func Renew(ctx context.Context, hc *http.Client, node string, creds []byte, now time.Time) (Credentials, error) {
	base, err := Endpoint(node)
	if err != nil {
		return Credentials{}, err
	}
	kp, err := jwt.ParseDecoratedUserNKey(creds)
	if err != nil {
		return Credentials{}, errors.New("edgejoin: the credentials file has no key")
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return Credentials{}, errors.New("edgejoin: the credentials file has no key")
	}
	at := now.Unix()
	sig, err := kp.Sign(RenewMessage(pub, at))
	if err != nil {
		return Credentials{}, fmt.Errorf("edgejoin: sign: %w", err)
	}
	ans, err := post(ctx, hc, base, RenewPath, RenewRequest{PublicKey: pub, At: at, Signature: base64.StdEncoding.EncodeToString(sig)})
	if err != nil {
		return Credentials{}, err
	}
	return finish(kp, base, ans)
}

func finish(kp nkeys.KeyPair, base *url.URL, ans Answer) (Credentials, error) {
	seed, err := kp.Seed()
	if err != nil {
		return Credentials{}, fmt.Errorf("edgejoin: seed: %w", err)
	}
	creds, err := jwt.FormatUserConfig(ans.JWT, seed)
	if err != nil {
		return Credentials{}, errors.New("edgejoin: the node's answer holds no usable JWT")
	}
	bus := ans.BusURL
	if bus == "" {
		bus = BusURLOf(base)
	}
	return Credentials{Name: ans.Name, Creds: creds, Operator: ans.Operator, BusURL: bus, Expires: ans.Expires}, nil
}

func post(ctx context.Context, hc *http.Client, base *url.URL, path string, body any) (Answer, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return Answer{}, err
	}
	u := *base
	u.Path += path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		return Answer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Answer{}, fmt.Errorf("edgejoin: the node did not answer: %w", urlErrorOnly(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return Answer{}, fmt.Errorf("edgejoin: read the answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e errorBody
		_ = json.Unmarshal(raw, &e)
		return Answer{}, &RefusedError{Status: resp.StatusCode, Words: e.Error}
	}
	var ans Answer
	if err := json.Unmarshal(raw, &ans); err != nil || ans.JWT == "" {
		return Answer{}, errors.New("edgejoin: the node's answer is unreadable")
	}
	return ans, nil
}

// urlErrorOnly drops the request URL from a transport error: it is the
// node's address, which the edge's output should not repeat.
func urlErrorOnly(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// RefusedError is the node's refusal: its status and its words.
type RefusedError struct {
	Status int
	Words  string
}

func (e *RefusedError) Error() string {
	if e.Words == "" {
		return fmt.Sprintf("edgejoin: the node refused (%d)", e.Status)
	}
	return fmt.Sprintf("edgejoin: the node refused: %s (%d)", e.Words, e.Status)
}
