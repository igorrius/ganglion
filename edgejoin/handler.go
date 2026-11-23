package edgejoin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

// Routes of the public listener (ADR-0116).
const (
	JoinPath  = "/edge/join"
	RenewPath = "/edge/renew"
)

// maxRequest bounds a request body; the largest real one is under 300 bytes.
const maxRequest = 4 << 10

// JoinRequest is the body of POST /edge/join.
type JoinRequest struct {
	Code      string `json:"code"`
	PublicKey string `json:"public_key"`
}

// RenewRequest is the body of POST /edge/renew.
type RenewRequest struct {
	PublicKey string `json:"public_key"`
	At        int64  `json:"at"`
	Signature string `json:"signature"`
}

// errorBody is every refusal's body: fixed words, never the request's text.
type errorBody struct {
	Error string `json:"error"`
}

// JoinHandler serves POST /edge/join.
func (s *Service) JoinHandler(log *slog.Logger) http.Handler {
	return s.handler(log, "join", func(r *http.Request, body []byte) (Answer, error) {
		var q JoinRequest
		if err := decode(body, &q); err != nil {
			return Answer{}, errBadRequest
		}
		return s.Join(r.Context(), q.Code, q.PublicKey)
	})
}

// RenewHandler serves POST /edge/renew.
func (s *Service) RenewHandler(log *slog.Logger) http.Handler {
	return s.handler(log, "renew", func(r *http.Request, body []byte) (Answer, error) {
		var q RenewRequest
		if err := decode(body, &q); err != nil {
			return Answer{}, errBadRequest
		}
		return s.Renew(r.Context(), q.PublicKey, q.At, q.Signature)
	})
}

var errBadRequest = errors.New("edgejoin: bad request")

func decode(body []byte, v any) error {
	// Unknown fields are refused: nothing rides along unread.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errBadRequest
	}
	return nil
}

func (s *Service) handler(log *slog.Logger, what string, do func(*http.Request, []byte) (Answer, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			write(w, http.StatusMethodNotAllowed, errorBody{"post only"})
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequest))
		if err != nil {
			write(w, http.StatusBadRequest, errorBody{"bad request"})
			return
		}
		ans, err := do(r, body)
		if err == nil {
			write(w, http.StatusOK, ans)
			return
		}
		code, word := status(err)
		if code >= 500 {
			// The cause stays in the log, without request text.
			log.Error("edgejoin: "+what+" failed", "err", err)
		}
		if code == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "12")
		}
		write(w, code, errorBody{word})
	})
}

// status is the HTTP status and the fixed words of an error.
func status(err error) (int, string) {
	switch {
	case errors.Is(err, ErrLimited):
		return http.StatusTooManyRequests, "too many attempts"
	case errors.Is(err, ErrBadCode):
		return http.StatusUnauthorized, "the code is not valid"
	case errors.Is(err, ErrBadSignature), errors.Is(err, ErrUnknownEdge):
		return http.StatusUnauthorized, "not allowed"
	case errors.Is(err, ErrRevoked):
		return http.StatusForbidden, "the key is revoked"
	case errors.Is(err, ErrBadKey), errors.Is(err, errBadRequest):
		return http.StatusBadRequest, "bad request"
	case errors.Is(err, ErrNameTaken):
		return http.StatusConflict, "an edge has this name"
	case errors.Is(err, ErrKeyTaken):
		return http.StatusConflict, "an edge has this key"
	case errors.Is(err, ErrStale):
		return http.StatusConflict, "the permissions changed since the card; ask for a new code"
	case errors.Is(err, ErrTooMany):
		return http.StatusServiceUnavailable, "try again later"
	}
	return http.StatusInternalServerError, "the node could not do it"
}

func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
