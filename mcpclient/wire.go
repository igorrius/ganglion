package mcpclient

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/igorrius/ganglion/internal/text"
)

// jsonrpcVersion is the "jsonrpc" member of every message.
const jsonrpcVersion = "2.0"

// jsonNull is the JSON null as raw text.
const jsonNull = "null"

// maxErrorMessage is the longest server error message kept, in runes.
const maxErrorMessage = 512

// maxEchoedID is the longest id of a server's request the client answers:
// the answer repeats it.
const maxEchoedID = 256

// JSON-RPC error codes the client answers with.
const codeMethodNotFound = -32601

// request is a JSON-RPC request or, without an id, a notification.
type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// reply is the client's answer to a request of the server's.
type reply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// message is any JSON-RPC message the server sends.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
}

// hasID reports whether the message has an id other than null.
func (m message) hasID() bool {
	id := bytes.TrimSpace(m.ID)
	return len(id) > 0 && string(id) != jsonNull
}

// answers reports whether the message's id is exactly the integer id: our
// ids are integers, so a string "7" is not the answer to 7.
func (m message) answers(id int64) bool {
	return string(bytes.TrimSpace(m.ID)) == strconv.FormatInt(id, 10)
}

// cutError returns e with its message cut.
func cutError(e *RPCError) *RPCError {
	return &RPCError{Code: e.Code, Message: text.Truncate(e.Message, maxErrorMessage)}
}
