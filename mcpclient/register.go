package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/oauth2"
)

// Token endpoint authentication methods (RFC 7591, 2) the node can use.
const (
	authMethodNone  = "none"
	authMethodPost  = "client_secret_post"
	authMethodBasic = "client_secret_basic"
)

// registration is the node's client at one authorization server, as the
// store keeps it in the sign-in's record: what dynamic registration gave and the
// endpoints discovery found, so a refresh after a restart needs no
// discovery.
type registration struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	AuthMethod   string `json:"token_endpoint_auth_method"`
	Redirect     string `json:"redirect_uri"`
	Resource     string `json:"resource"`
	Scope        string `json:"scope,omitempty"`
	Issuer       string `json:"issuer"`
	IssParameter bool   `json:"iss_parameter,omitempty"`
	AuthURL      string `json:"authorization_endpoint"`
	TokenURL     string `json:"token_endpoint"`
	RevokeURL    string `json:"revocation_endpoint,omitempty"`
}

// config is the x/oauth2 configuration of the client.
func (r registration) config(redirect string) *oauth2.Config {
	style := oauth2.AuthStyleInParams
	if r.AuthMethod == authMethodBasic {
		style = oauth2.AuthStyleInHeader
	}
	return &oauth2.Config{ClientID: r.ClientID, ClientSecret: r.ClientSecret, RedirectURL: redirect,
		Endpoint: oauth2.Endpoint{AuthURL: r.AuthURL, TokenURL: r.TokenURL, AuthStyle: style},
		Scopes:   strings.Fields(r.Scope)}
}

// fits reports whether a stored registration serves this sign-in: the same
// issuer, resource and redirect address.
func (r registration) fits(md Metadata, redirect string) bool {
	return r.ClientID != "" && r.Issuer == md.Issuer && r.Resource == md.Resource && r.Redirect == redirect
}

// withEndpoints is r with the endpoints and the scope discovery found now.
func (r registration) withEndpoints(md Metadata) registration {
	r.Scope, r.IssParameter = md.Scope, md.IssParameter
	r.AuthURL, r.TokenURL, r.RevokeURL = md.AuthorizationEndpoint, md.TokenEndpoint, md.RevocationEndpoint
	return r
}

// registration is the server's registration for this sign-in: the one of
// the stored sign-in when it fits, else a new one (RFC 7591). Nothing is
// stored here: a registration is stored with the tokens it gets, when the
// sign-in completes, so an abandoned sign-in leaves the stored one as it
// was.
func (o *OAuth) registration(ctx context.Context, server string, md Metadata) (registration, error) {
	reg, err := o.storedRegistration(ctx, server)
	if err != nil {
		return registration{}, err
	}
	if !reg.fits(md, o.redirect) {
		reg, err = o.register(ctx, md)
		o.in.step(ctx, stepRegister, err)
		if err != nil {
			return registration{}, err
		}
	}
	return reg.withEndpoints(md), nil
}

// storedRegistration is the registration of the stored sign-in; zero for
// none or for one that does not parse.
func (o *OAuth) storedRegistration(ctx context.Context, server string) (registration, error) {
	var rec signInRecord
	if _, err := o.loadJSON(ctx, server, nameSignIn, &rec); err != nil {
		return registration{}, err
	}
	return rec.Client, nil
}

// registerRequest is the client metadata the node registers (RFC 7591,
// 2): a public client whose only redirect address is the intake page.
type registerRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope,omitempty"`
}

// registerAnswer is what the authorization server registered (RFC 7591,
// 3.2.1).
type registerAnswer struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	RedirectURIs            []string `json:"redirect_uris"`
}

// register registers the node as a new client.
func (o *OAuth) register(ctx context.Context, md Metadata) (registration, error) {
	body, err := json.Marshal(registerRequest{ClientName: o.cfg.ClientName, RedirectURIs: []string{o.redirect},
		GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
		TokenEndpointAuthMethod: authMethodNone, Scope: md.Scope})
	if err != nil {
		return registration{}, fmt.Errorf("%w: encode the request", ErrRegistration)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return registration{}, fmt.Errorf("%w: build the request", ErrRegistration)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := o.hc.Do(req)
	if err != nil {
		return registration{}, o.requestFailure(ctx, err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return registration{}, fmt.Errorf("%w: the server answered status %d", ErrRegistration, resp.StatusCode)
	}
	var a registerAnswer
	if err := readJSON(resp, &a); err != nil {
		return registration{}, fmt.Errorf("%w: %w", ErrRegistration, err)
	}
	return o.registered(a, md)
}

// registered checks the answer: a client id, the node's redirect address
// kept as given, an authentication method the node can use.
func (o *OAuth) registered(a registerAnswer, md Metadata) (registration, error) {
	method := a.TokenEndpointAuthMethod
	if method == "" {
		method = authMethodNone
	}
	switch {
	case a.ClientID == "" || len(a.ClientID) > maxSessionID:
		return registration{}, fmt.Errorf("%w: no usable client id", ErrRegistration)
	case len(a.RedirectURIs) > 0 && !slices.Contains(a.RedirectURIs, o.redirect):
		return registration{}, fmt.Errorf("%w: the redirect address was changed", ErrRegistration)
	case !slices.Contains([]string{authMethodNone, authMethodPost, authMethodBasic}, method):
		return registration{}, fmt.Errorf("%w: an authentication method the node does not use", ErrRegistration)
	case method != authMethodNone && a.ClientSecret == "":
		return registration{}, fmt.Errorf("%w: a confidential client without a secret", ErrRegistration)
	}
	return registration{ClientID: a.ClientID, ClientSecret: a.ClientSecret, AuthMethod: method,
		Redirect: o.redirect, Resource: md.Resource, Issuer: md.Issuer}, nil
}
