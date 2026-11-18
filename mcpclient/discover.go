package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/internal/urlnorm"
)

// Limits of what discovery reads.
const (
	// maxMetadata is the biggest metadata document or registration answer.
	maxMetadata = 64 << 10
	// maxAuthServers is how many authorization servers of a resource are tried.
	maxAuthServers = 4
	// maxScope is the longest scope taken from a challenge or a document.
	maxScope = 1 << 10
)

// Well-known paths (RFC 9728, RFC 8414, OpenID Connect Discovery).
const (
	wellKnownResource = "/.well-known/oauth-protected-resource"
	wellKnownAuth     = "/.well-known/oauth-authorization-server"
	wellKnownOpenID   = "/.well-known/openid-configuration"
)

// Metadata is what discovery found about a server's sign-in: its protected
// resource's metadata (RFC 9728) and its authorization server's (RFC 8414).
// Every address in it passed the same check as the server's.
type Metadata struct {
	// Resource is the server's identifier, sent as `resource` (RFC 8707).
	Resource string
	// Issuer is the authorization server's identifier.
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	RegistrationEndpoint  string
	RevocationEndpoint    string // "" when the server names none
	// IssParameter says the authorization server sends `iss` with the
	// code (RFC 9207), so a sign-in's answer must carry it.
	IssParameter bool
	// Scope is what the sign-in asks for: the challenge's scope, else the
	// resource's scopes_supported; "" asks for none.
	Scope string
}

// SignInHost is the host the admin signs in at, for the card.
func (m Metadata) SignInHost() string { return hostOf(m.AuthorizationEndpoint) }

// hostOf is the host of an address that passed checkAddress.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// metadataError is discovery that found nothing usable: the reason is the
// client's own words, never an address or a body.
func metadataError(reason string) error { return fmt.Errorf("%w: %s", ErrMetadata, reason) }

// checkAddress is the check every address of the sign-in passes, the one
// web.fetch makes before the network (R-MCP-3): an absolute https address
// with a host and no user info, normalised. Whether the host is public is
// the dialer's check (web.PublicDial), so it covers every hop.
func checkAddress(raw string) (*url.URL, error) {
	norm, ok := urlnorm.Normalize(raw)
	if !ok {
		return nil, metadataError("an address that is not absolute")
	}
	u, err := url.Parse(norm)
	switch {
	case err != nil:
		return nil, metadataError("an address that does not parse")
	case u.Scheme != schemeHTTPS:
		return nil, metadataError("an address that is not https")
	case u.User != nil:
		return nil, metadataError("an address with user info")
	}
	return u, nil
}

// discover finds the sign-in of the server at serverURL: a request without
// a credential must be refused with 401, whose challenge names the
// resource's metadata (RFC 9728, 5.1), or the well-known documents are
// tried, the path-suffixed one first. A server that answers without a
// sign-in is ErrNoSignIn.
func (o *OAuth) discover(ctx context.Context, serverURL string) (Metadata, error) {
	server, err := checkAddress(serverURL)
	if err != nil {
		return Metadata{}, err
	}
	ch, err := o.probe(ctx, server.String())
	if err != nil {
		return Metadata{}, err
	}
	res, err := o.resourceMetadata(ctx, server, ch.metadata)
	if err != nil {
		return Metadata{}, err
	}
	md, err := o.authMetadataOf(ctx, res.AuthorizationServers)
	if err != nil {
		return Metadata{}, err
	}
	md.Resource = res.Resource
	md.Scope = firstNonEmpty(ch.scope, strings.Join(res.ScopesSupported, " "))
	if len(md.Scope) > maxScope {
		return Metadata{}, metadataError("a scope too long")
	}
	return md, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// challenge is what a 401 said: where the resource's metadata is, and the
// scope it wants.
type challenge struct{ metadata, scope string }

// probe sends initialize with no credential and reads the challenge of
// the 401 it must get.
func (o *OAuth) probe(ctx context.Context, serverURL string) (challenge, error) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` +
		ProtocolVersion + `","capabilities":{},"clientInfo":{"name":"ganglion","version":"` + o.version + `"}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, bytes.NewReader(body))
	if err != nil {
		return challenge{}, metadataError("a request that cannot be built")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := o.hc.Do(req)
	if err != nil {
		return challenge{}, o.requestFailure(ctx, err)
	}
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		return challenge{}, ErrNoSignIn
	}
	params := bearerParams(resp.Header.Values("WWW-Authenticate"))
	if len(params["scope"]) > maxScope {
		return challenge{}, metadataError("a scope too long")
	}
	return challenge{metadata: params["resource_metadata"], scope: params["scope"]}, nil
}

// requestFailure is a sign-in request that got no answer, in the client's
// own words: Go's text would carry the address.
func (o *OAuth) requestFailure(ctx context.Context, err error) error {
	if errors.Is(err, ErrRedirect) {
		return metadataError("a redirect")
	}
	if ctx.Err() != nil {
		return &transportError{text: "the sign-in server did not answer in time", timeout: true}
	}
	return &transportError{text: "the sign-in server is unreachable"}
}

// resourceDoc is a protected resource's metadata (RFC 9728, 2).
type resourceDoc struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// resourceMetadata reads the resource's metadata. An address the challenge
// named must be on the server's own origin: a server does not get to send
// the node to read another host's document. Without one, the path-suffixed
// well-known document is tried, then the bare one.
func (o *OAuth) resourceMetadata(ctx context.Context, server *url.URL, named string) (resourceDoc, error) {
	candidates := resourceCandidates(server)
	if named != "" {
		u, err := checkAddress(named)
		if err != nil {
			return resourceDoc{}, err
		}
		if originOf(u) != originOf(server) {
			return resourceDoc{}, metadataError("the resource's metadata is on another host")
		}
		candidates = []string{u.String()}
	}
	for _, c := range candidates {
		var doc resourceDoc
		found, err := o.getJSON(ctx, c, &doc)
		if err != nil {
			return resourceDoc{}, err
		}
		if found {
			return doc, checkResource(doc, server)
		}
	}
	return resourceDoc{}, ErrNoSignIn
}

// resourceCandidates are the well-known addresses of the resource's
// metadata (RFC 9728, 3.1): the one with the server's path, then the bare
// one.
func resourceCandidates(server *url.URL) []string {
	root := server.Scheme + "://" + server.Host + wellKnownResource
	if p := strings.TrimSuffix(server.Path, "/"); p != "" {
		return []string{root + p, root}
	}
	return []string{root}
}

// checkResource refuses a document that is not about this server (RFC
// 9728, 3.3): its resource must be the server's address or a path above it
// on the same origin.
func checkResource(doc resourceDoc, server *url.URL) error {
	u, err := checkAddress(doc.Resource)
	if err != nil {
		return err
	}
	if originOf(u) != originOf(server) || !pathUnder(server.Path, u.Path) {
		return metadataError("the resource's metadata is about another resource")
	}
	if len(doc.AuthorizationServers) == 0 {
		return metadataError("the resource names no authorization server")
	}
	return nil
}

// pathUnder reports whether path is base or below it, by whole segments.
func pathUnder(path, base string) bool {
	base = strings.TrimSuffix(base, "/")
	path = strings.TrimSuffix(path, "/")
	return base == "" || path == base || strings.HasPrefix(path, base+"/")
}

// authDoc is an authorization server's metadata (RFC 8414, 2).
type authDoc struct {
	Issuer                 string   `json:"issuer"`
	AuthorizationEndpoint  string   `json:"authorization_endpoint"`
	TokenEndpoint          string   `json:"token_endpoint"`
	RegistrationEndpoint   string   `json:"registration_endpoint"`
	RevocationEndpoint     string   `json:"revocation_endpoint"`
	ResponseTypes          []string `json:"response_types_supported"`
	CodeChallengeMethods   []string `json:"code_challenge_methods_supported"`
	GrantTypes             []string `json:"grant_types_supported"`
	IssParameterSupported  bool     `json:"authorization_response_iss_parameter_supported"`
	TokenEndpointAuthMeths []string `json:"token_endpoint_auth_methods_supported"`
}

// authMetadataOf reads the metadata of the first authorization server that
// has a usable one.
func (o *OAuth) authMetadataOf(ctx context.Context, issuers []string) (Metadata, error) {
	err := metadataError("no usable authorization server")
	for _, issuer := range issuers[:min(len(issuers), maxAuthServers)] {
		var md Metadata
		md, err = o.authMetadata(ctx, issuer)
		if err == nil {
			return md, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Metadata{}, err
		}
	}
	return Metadata{}, err
}

// authMetadata reads one authorization server's metadata from its
// well-known addresses (RFC 8414, 3.1, and OpenID Connect Discovery).
func (o *OAuth) authMetadata(ctx context.Context, issuer string) (Metadata, error) {
	iu, err := checkAddress(issuer)
	if err != nil {
		return Metadata{}, err
	}
	if iu.RawQuery != "" {
		return Metadata{}, metadataError("an issuer with a query")
	}
	for _, c := range authCandidates(iu) {
		var doc authDoc
		found, err := o.getJSON(ctx, c, &doc)
		if err != nil {
			return Metadata{}, err
		}
		if found {
			return metadataOf(doc, iu)
		}
	}
	return Metadata{}, metadataError("the authorization server has no metadata")
}

// authCandidates are the addresses of an issuer's metadata: the well-known
// path inserted before the issuer's path (RFC 8414), the same for OpenID
// Connect, and OpenID Connect's appended one.
func authCandidates(issuer *url.URL) []string {
	root := issuer.Scheme + "://" + issuer.Host
	p := strings.TrimSuffix(issuer.Path, "/")
	if p == "" {
		return []string{root + wellKnownAuth, root + wellKnownOpenID}
	}
	return []string{root + wellKnownAuth + p, root + wellKnownOpenID + p, root + p + wellKnownOpenID}
}

// metadataOf checks an authorization server's document: the issuer it
// names is the one asked for (RFC 8414, 3.3), every endpoint passes the
// address check, it serves codes with PKCE S256 and registers clients.
func metadataOf(doc authDoc, issuer *url.URL) (Metadata, error) {
	if norm, ok := urlnorm.Normalize(doc.Issuer); !ok || norm != issuer.String() {
		return Metadata{}, metadataError("the metadata names another issuer")
	}
	switch {
	case doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "":
		return Metadata{}, metadataError("the metadata lacks an endpoint")
	case doc.RegistrationEndpoint == "":
		return Metadata{}, metadataError("the authorization server does not register clients")
	case !slices.Contains(doc.CodeChallengeMethods, "S256"):
		return Metadata{}, metadataError("the authorization server does not support PKCE S256")
	case len(doc.ResponseTypes) > 0 && !slices.Contains(doc.ResponseTypes, "code"):
		return Metadata{}, metadataError("the authorization server does not issue codes")
	}
	md := Metadata{Issuer: doc.Issuer, IssParameter: doc.IssParameterSupported}
	for _, f := range []struct {
		dst *string
		raw string
	}{
		{&md.AuthorizationEndpoint, doc.AuthorizationEndpoint},
		{&md.TokenEndpoint, doc.TokenEndpoint},
		{&md.RegistrationEndpoint, doc.RegistrationEndpoint},
		{&md.RevocationEndpoint, doc.RevocationEndpoint},
	} {
		if f.raw == "" {
			continue
		}
		u, err := checkAddress(f.raw)
		if err != nil {
			return Metadata{}, err
		}
		*f.dst = u.String()
	}
	return md, nil
}

// getJSON reads a JSON document within maxMetadata. found is false for a
// 404 or 410: the caller tries the next address. Any other failure is an
// error.
func (o *OAuth) getJSON(ctx context.Context, raw string, into any) (found bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, http.NoBody)
	if err != nil {
		return false, metadataError("a request that cannot be built")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := o.hc.Do(req)
	if err != nil {
		return false, o.requestFailure(ctx, err)
	}
	defer drainClose(resp.Body)
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return false, nil
	case resp.StatusCode != http.StatusOK:
		return false, metadataError(fmt.Sprintf("a metadata document answered status %d", resp.StatusCode))
	}
	return true, readJSON(resp, into)
}

// readJSON decodes a JSON body of at most maxMetadata bytes.
func readJSON(resp *http.Response, into any) error {
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "application/json" {
		return metadataError("a document that is not JSON")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadata+1))
	switch {
	case err != nil:
		return &transportError{text: "the sign-in server's answer broke off"}
	case len(data) > maxMetadata:
		return fmt.Errorf("%w: a document over %d bytes", ErrTooLarge, maxMetadata)
	case json.Unmarshal(data, into) != nil:
		return metadataError("a document that does not parse")
	}
	return nil
}

// bearerParams are the parameters of the Bearer challenge among the
// WWW-Authenticate values (RFC 9110, 11.6.1); other schemes are skipped.
func bearerParams(values []string) map[string]string {
	out := map[string]string{}
	for _, v := range values {
		scheme := ""
		for _, p := range splitChallenges(v) {
			if p.scheme != "" {
				scheme = strings.ToLower(p.scheme)
				continue
			}
			if scheme == "bearer" {
				if _, ok := out[p.name]; !ok {
					out[p.name] = p.value
				}
			}
		}
	}
	return out
}

// authItem is a scheme or one of its parameters.
type authItem struct{ scheme, name, value string }

// splitChallenges reads a WWW-Authenticate value as schemes and their
// name=value parameters, the value a token or a quoted string.
func splitChallenges(s string) []authItem {
	var out []authItem
	for i := 0; i < len(s); {
		i = skipSeparators(s, i)
		tok, j := readToken(s, i)
		if tok == "" {
			break
		}
		k := skipSpaces(s, j)
		if k < len(s) && s[k] == '=' && !strings.HasPrefix(s[k:], "==") {
			val, next := readValue(s, skipSpaces(s, k+1))
			out = append(out, authItem{name: strings.ToLower(tok), value: val})
			i = next
			continue
		}
		out = append(out, authItem{scheme: tok})
		i = j
	}
	return out
}

func skipSeparators(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == ',' || s[i] == '=') {
		i++
	}
	return i
}

func skipSpaces(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// readToken reads a token (or a token68, which may end in '=') at i.
func readToken(s string, i int) (string, int) {
	j := i
	for j < len(s) && !strings.ContainsRune(" \t,=\"", rune(s[j])) {
		j++
	}
	return s[i:j], j
}

// readValue reads a token or a quoted string at i.
func readValue(s string, i int) (string, int) {
	if i >= len(s) || s[i] != '"' {
		return readToken(s, i)
	}
	var b strings.Builder
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			if j+1 < len(s) {
				j++
				b.WriteByte(s[j])
			}
		case '"':
			return b.String(), j + 1
		default:
			b.WriteByte(s[j])
		}
	}
	return b.String(), len(s)
}
