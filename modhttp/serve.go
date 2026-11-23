package modhttp

import (
	"context"
	"errors"
	"html/template"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/igorrius/ganglion/pdk"
)

// ServeHTTP implements http.Handler: the checks in order, each a real
// refusal with its status, then the module's run.
func (l *Listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, h := range nodeHeaders {
		w.Header().Set(h[0], h[1])
	}
	select {
	case l.slot <- struct{}{}:
		defer func() { <-l.slot }()
	default:
		w.Header().Set("Retry-After", "1")
		refuse(w, http.StatusServiceUnavailable, "too many requests at once")
		return
	}
	if !l.ownHost(r.Host) {
		// DNS rebinding: a foreign name that resolves to loopback (R-HTTP-3).
		refuse(w, http.StatusForbidden, "this listener answers only its own address")
		return
	}
	if !l.sameOrigin(r) {
		refuse(w, http.StatusForbidden, "a request from another site is refused")
		return
	}
	segs, ok := cleanPath(r)
	if !ok {
		refuse(w, http.StatusBadRequest, "the path is not plain")
		return
	}
	if len(segs) == 0 {
		l.index(w, r)
		return
	}
	if len(segs) == 1 { // /<module> without the slash: its page is /<module>/
		l.redirect(w, segs[0])
		return
	}
	l.module(w, r, segs[0], "/"+strings.Join(segs[1:], "/"))
}

// ownHost reports whether host is the listener's own address, compared
// without case.
func (l *Listener) ownHost(host string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hosts[strings.ToLower(host)]
}

// sameOrigin refuses what another site makes a browser send (ADR-0118):
// an Origin that is not the listener's own, a write that the browser marks
// as from another site, and a read from another site that is not the
// owner opening the page (a script, an image or a fetch of it).
func (l *Listener) sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		host, ok := strings.CutPrefix(o, "http://")
		if !ok || !l.ownHost(host) {
			return false
		}
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", sameOrigin, "none":
		return true
	}
	return r.Method == http.MethodGet && r.Header.Get("Sec-Fetch-Mode") == "navigate"
}

// cleanPath splits the request's path into its segments, or refuses it:
// an encoded slash or backslash, a backslash, a control byte, a dot
// segment, or an empty segment anywhere but at the end. A trailing slash
// leaves an empty last segment, so "/m/" is ["m", ""].
func cleanPath(r *http.Request) ([]string, bool) {
	raw := strings.ToLower(r.URL.RawPath)
	if strings.Contains(raw, "%2f") || strings.Contains(raw, "%5c") {
		return nil, false
	}
	p := r.URL.Path
	if !strings.HasPrefix(p, "/") || strings.ContainsFunc(p, func(c rune) bool { return c < 0x20 || c == 0x7f || c == '\\' }) {
		return nil, false
	}
	if p == "/" {
		return nil, true
	}
	segs := strings.Split(p[1:], "/")
	for i, s := range segs {
		if s == "." || s == ".." || (s == "" && i < len(segs)-1) {
			return nil, false
		}
	}
	return segs, true
}

// index lists the pages of the running modules.
func (l *Listener) index(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		refuse(w, http.StatusMethodNotAllowed, "the list of pages answers GET only")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := indexPage.Execute(w, l.mods.Pages()); err != nil {
		l.log.Debug("modhttp: the list of pages was not written", "err", err)
	}
}

// indexPage is the list at /: escaped by html/template, no script and no
// inline style, so the policy holds for it too.
var indexPage = template.Must(template.New("index").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Pages</title></head><body>
<h1>Pages</h1>
{{if .}}<ul>{{range .}}<li><a href="/{{.Short}}/">{{.Short}}</a></li>{{end}}</ul>{{else}}<p>No module serves a page.</p>{{end}}
</body></html>
`))

// redirect sends /<module> to /<module>/ when such a page runs; the name
// in Location is the module's own, never the request's bytes.
func (l *Listener) redirect(w http.ResponseWriter, short string) {
	for _, p := range l.mods.Pages() {
		if p.Short == short {
			w.Header().Set("Location", "/"+p.Short+"/")
			w.WriteHeader(http.StatusPermanentRedirect)
			return
		}
	}
	refuse(w, http.StatusNotFound, "no such page")
}

// module runs the request as the module's and writes its answer.
func (l *Listener) module(w http.ResponseWriter, r *http.Request, short, path string) {
	if r.ContentLength > MaxBody {
		refuse(w, http.StatusRequestEntityTooLarge, "the body is longer than 1 MiB")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			refuse(w, http.StatusRequestEntityTooLarge, "the body is longer than 1 MiB")
			return
		}
		refuse(w, http.StatusBadRequest, "the body could not be read")
		return
	}
	req := pdk.HTTPRequest{Method: r.Method, Path: path, Query: r.URL.RawQuery, Headers: pick(r.Header, requestHeaders), Body: body}
	// The run does not end when the browser goes away: a run cut short is
	// counted as the module's failure, and a page that polls would turn its
	// module off. The module's own time limit bounds it.
	ans, err := l.mods.ServeHTTP(context.WithoutCancel(r.Context()), short, req)
	if err != nil {
		l.refusal(w, err)
		return
	}
	write(w, ans)
}

// refusal writes the status of a refusal of the module host.
func (l *Listener) refusal(w http.ResponseWriter, err error) {
	if me, ok := errors.AsType[*MethodError](err); ok {
		w.Header().Set("Allow", strings.Join(me.Allow, ", "))
		refuse(w, http.StatusMethodNotAllowed, "the route does not answer this method")
		return
	}
	switch {
	case errors.Is(err, ErrNoPage):
		refuse(w, http.StatusNotFound, "no such page")
	case errors.Is(err, ErrBusy):
		w.Header().Set("Retry-After", "1")
		refuse(w, http.StatusTooManyRequests, "the module is busy")
	case errors.Is(err, ErrTimedOut):
		refuse(w, http.StatusGatewayTimeout, "the module ran past its time limit")
	case errors.Is(err, ErrFailed):
		refuse(w, http.StatusBadGateway, "the module failed")
	default:
		l.log.Warn("modhttp: a request was not run", "err", err)
		refuse(w, http.StatusInternalServerError, "the node could not run the module")
	}
}

// write writes a module's answer: its status, the headers it may set, and
// its body. A status the node did not expect is a failure: the module host
// checked it already.
func write(w http.ResponseWriter, a pdk.HTTPAnswer) {
	if a.Status < 200 || a.Status > 599 || len(a.Body) > MaxAnswerBody {
		refuse(w, http.StatusBadGateway, "the module failed")
		return
	}
	for name, v := range a.Headers {
		name = strings.ToLower(strings.TrimSpace(name))
		if slices.Contains(answerHeaders, name) && validValue(v) {
			w.Header().Set(name, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	if a.Status == http.StatusNoContent || a.Status == http.StatusNotModified {
		w.WriteHeader(a.Status)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(a.Body)))
	w.WriteHeader(a.Status)
	_, _ = w.Write(a.Body)
}

// pick are the headers of h named in names, lower-case, each value valid
// and within maxHeaderValue; nil when there are none.
func pick(h http.Header, names []string) map[string]string {
	var out map[string]string
	for _, n := range names {
		if v := h.Get(n); v != "" && validValue(v) {
			if out == nil {
				out = map[string]string{}
			}
			out[n] = v
		}
	}
	return out
}

// validValue reports whether v may be a header value: printable ASCII or a
// tab, within maxHeaderValue.
func validValue(v string) bool {
	if len(v) > maxHeaderValue {
		return false
	}
	return !strings.ContainsFunc(v, func(c rune) bool { return (c < 0x20 && c != '\t') || c >= 0x7f })
}

// refuse writes a refusal: the status and the node's words, as plain text.
func refuse(w http.ResponseWriter, status int, words string) {
	h := w.Header()
	for _, n := range answerHeaders {
		h.Del(n)
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, words+"\n")
}
