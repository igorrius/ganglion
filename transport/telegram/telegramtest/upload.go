package telegramtest

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
	"unicode/utf16"
)

// MaxCaptionLen is the longest caption the fake accepts, in UTF-16 code
// units, as Telegram does.
const MaxCaptionLen = 1024

// Upload is a picture or a document the bot sent (sendPhoto,
// sendDocument).
type Upload struct {
	Method   string // sendPhoto or sendDocument
	ChatID   int64
	ThreadID int64
	Caption  string
	Entities string // caption_entities as sent, JSON; "" when none
	Silent   bool
	FileName string
	Data     []byte
}

// formFile is the file of a multipart request.
type formFile struct {
	field, name string
	data        []byte
}

// rawFields are the form fields that carry JSON, not a plain string.
var rawFields = map[string]bool{
	"chat_id": true, "message_thread_id": true, "caption_entities": true,
	"reply_parameters": true, "disable_notification": true,
}

// readForm reads a multipart request into the same parameter map a JSON
// request gives, and its one file.
func readForm(r *http.Request) (map[string]json.RawMessage, *formFile, error) {
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		return nil, nil, err
	}
	params := map[string]json.RawMessage{}
	for k, vs := range r.MultipartForm.Value {
		if len(vs) == 0 {
			continue
		}
		if rawFields[k] {
			params[k] = json.RawMessage(vs[0])
			continue
		}
		raw, _ := json.Marshal(vs[0])
		params[k] = raw
	}
	for field, fhs := range r.MultipartForm.File {
		f, err := fhs[0].Open()
		if err != nil {
			return nil, nil, err
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			return nil, nil, err
		}
		return params, &formFile{field: field, name: fhs[0].Filename, data: data}, nil
	}
	return params, nil, nil
}

// sendFile records an upload and answers with the message it made.
func (s *Server) sendFile(w http.ResponseWriter, method string, params map[string]json.RawMessage, file *formFile) {
	var p struct {
		ChatID   int64           `json:"chat_id"`
		ThreadID int64           `json:"message_thread_id"`
		Caption  string          `json:"caption"`
		Entities json.RawMessage `json:"caption_entities"`
		Silent   bool            `json:"disable_notification"`
	}
	raw, _ := json.Marshal(params)
	if err := json.Unmarshal(raw, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "Bad Request: "+err.Error(), 0)
		return
	}
	want := map[string]string{"sendPhoto": "photo", "sendDocument": "document"}[method]
	switch {
	case p.ChatID == 0:
		writeErr(w, http.StatusBadRequest, "Bad Request: chat_id is empty", 0)
		return
	case file == nil || file.field != want:
		writeErr(w, http.StatusBadRequest, "Bad Request: there is no "+want+" in the request", 0)
		return
	case len(utf16.Encode([]rune(p.Caption))) > MaxCaptionLen:
		writeErr(w, http.StatusBadRequest, "Bad Request: message caption is too long", 0)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextMsg++
	s.uploads = append(s.uploads, Upload{Method: method, ChatID: p.ChatID, ThreadID: p.ThreadID, Caption: p.Caption,
		Entities: string(p.Entities), Silent: p.Silent, FileName: file.name, Data: file.data})
	writeOK(w, map[string]any{"message_id": s.nextMsg, "date": time.Now().Unix(),
		"chat": map[string]any{"id": p.ChatID, "type": "private"}, "caption": p.Caption})
}

// Uploads returns the pictures and documents the bot sent, in order.
func (s *Server) Uploads() []Upload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Upload(nil), s.uploads...)
}
