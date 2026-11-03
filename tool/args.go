package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
)

// ErrBadArgs is a call whose arguments are not a JSON object of the
// parameters the tool declares. Its text is fixed: the arguments may
// come from a page the model read.
var ErrBadArgs = errors.New("tool: the arguments must be a JSON object of the tool's declared parameters, each given once, names exactly as declared")

// CheckArgs checks args against the properties s.InputSchema declares:
// a JSON object whose keys are declared ones, matched exactly
// (case-sensitively), each given once. The policy reads arguments by
// their exact names and a tool decodes them into a struct, whose match
// is case-insensitive; refusing anything else first means both always
// see the same value (ruling R18). A spec that declares no properties
// accepts no call.
func CheckArgs(s Spec, args json.RawMessage) error {
	props, err := properties(s)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return ErrBadArgs
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok || seen[key] {
			return ErrBadArgs
		}
		if _, declared := props[key]; !declared {
			return ErrBadArgs
		}
		seen[key] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return ErrBadArgs
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return ErrBadArgs
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return ErrBadArgs
	}
	return nil
}

// Properties lists the parameter names s declares, or an error when its
// schema declares none.
func Properties(s Spec) ([]string, error) {
	props, err := properties(s)
	if err != nil {
		return nil, err
	}
	return slices.Sorted(maps.Keys(props)), nil
}

func properties(s Spec) (map[string]json.RawMessage, error) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(s.InputSchema, &schema); err != nil || schema.Properties == nil {
		return nil, ErrBadArgs
	}
	return schema.Properties, nil
}

// CanonicalArgs is args as canonical JSON: object keys sorted, no
// insignificant whitespace, numbers kept exactly as written. Two calls of
// one tool with the same canonical arguments are the same call (ruling
// R38). An error means args is not one JSON value.
func CanonicalArgs(args json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", ErrBadArgs
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", ErrBadArgs
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", ErrBadArgs
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}
