package chatlab

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

// postBot calls the fake as the node's adapter does, and returns the result.
func postBot(t *testing.T, fake *telegramtest.Server, method string, body map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return readResult(t, fake.URL()+"/bot"+testBot+"/"+method, "application/json", bytes.NewReader(raw))
}

// postPhoto sends a picture to the chat as the node's adapter does.
func postPhoto(t *testing.T, fake *telegramtest.Server, caption string, data []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", "42")
	_ = w.WriteField("caption", caption)
	part, err := w.CreateFormFile("photo", "dash.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	readResult(t, fake.URL()+"/bot"+testBot+"/sendPhoto", w.FormDataContentType(), &buf)
}

func readResult(t *testing.T, url, contentType string, body io.Reader) json.RawMessage {
	t.Helper()
	resp, err := http.Post(url, contentType, body) //nolint:gosec,noctx // the local fake
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		OK          bool
		Description string
		Result      json.RawMessage
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("%s: %s", url, out.Description)
	}
	return out.Result
}
