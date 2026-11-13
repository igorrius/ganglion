package telegramtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ForeignWebhook is the address of the webhook SetWebhook(true) sets:
// somebody else's.
const ForeignWebhook = "https://elsewhere.example/hook"

// SecretHeader carries the webhook's secret, as Telegram sends it.
const SecretHeader = "X-Telegram-Bot-Api-Secret-Token"

// WebhookSet is one successful setWebhook call.
type WebhookSet struct {
	URL            string
	Secret         string
	AllowedUpdates []string
	MaxConnections int
	DropPending    bool
}

// WebhookInfo is the answer of getWebhookInfo.
type WebhookInfo struct {
	URL                string `json:"url"`
	PendingUpdateCount int    `json:"pending_update_count"`
	LastErrorDate      int64  `json:"last_error_date,omitempty"`
	LastErrorMessage   string `json:"last_error_message,omitempty"`
}

// webhook is the webhook that is set and its deliveries.
type webhook struct {
	url, secret string
	stop        chan struct{} // closed when the webhook is replaced or removed
	lastErr     int64
	lastErrMsg  string
}

// WebhookSets are the successful setWebhook calls, in order.
func (s *Server) WebhookSets() []WebhookSet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]WebhookSet(nil), s.hookSets...)
}

// WebhookInfo is what getWebhookInfo answers now.
func (s *Server) WebhookInfo() WebhookInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := WebhookInfo{PendingUpdateCount: len(s.updates)}
	if s.hook != nil {
		info.URL, info.LastErrorDate, info.LastErrorMessage = s.hook.url, s.hook.lastErr, s.hook.lastErrMsg
	}
	return info
}

// SetWebhookRetry sets how long the fake waits before it sends a refused
// update again; the default is 20 ms, where Telegram waits seconds, then
// minutes.
func (s *Server) SetWebhookRetry(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hookRetry = d
}

// dropHook removes the webhook and stops its deliveries; the updates that
// wait are kept. The caller holds s.mu.
func (s *Server) dropHook() {
	if s.hook != nil {
		close(s.hook.stop)
		s.hook = nil
	}
}

func (s *Server) setWebhook(w http.ResponseWriter, params map[string]json.RawMessage) {
	var set WebhookSet
	_ = json.Unmarshal(params["url"], &set.URL)
	_ = json.Unmarshal(params["secret_token"], &set.Secret)
	_ = json.Unmarshal(params["allowed_updates"], &set.AllowedUpdates)
	_ = json.Unmarshal(params["max_connections"], &set.MaxConnections)
	_ = json.Unmarshal(params["drop_pending_updates"], &set.DropPending)
	if !strings.HasPrefix(set.URL, "http") {
		writeErr(w, http.StatusBadRequest, "Bad Request: bad webhook: an HTTPS URL must be provided for webhook", 0)
		return
	}
	s.mu.Lock()
	s.dropHook()
	h := &webhook{url: set.URL, secret: set.Secret, stop: make(chan struct{})}
	s.hook = h
	s.hookSets = append(s.hookSets, set)
	if set.AllowedUpdates != nil {
		s.allowed = set.AllowedUpdates
	}
	if set.DropPending {
		s.updates = nil
	}
	s.mu.Unlock()
	go s.deliver(h)
	writeOK(w, true)
}

// deliver sends the waiting updates to h one at a time, in order, until h
// is replaced or the server closes. An update leaves the queue only when
// its request was answered 2xx; any other answer is recorded as the last
// error and the same update is sent again after the retry wait.
func (s *Server) deliver(h *webhook) {
	hc := &http.Client{Timeout: 10 * time.Second}
	for {
		s.mu.Lock()
		ups, notify, retry := s.deliverable(), s.notify, s.hookRetry
		s.mu.Unlock()
		if len(ups) == 0 {
			select {
			case <-notify:
				continue
			case <-h.stop:
				return
			case <-s.closed:
				return
			}
		}
		failed := s.post(hc, h, ups[0])
		if failed == "" {
			s.confirm(ups[0]["update_id"].(int64))
			continue
		}
		s.mu.Lock()
		h.lastErr, h.lastErrMsg = time.Now().Unix(), failed
		s.mu.Unlock()
		select {
		case <-time.After(retry):
		case <-h.stop:
			return
		case <-s.closed:
			return
		}
	}
}

// post sends one update to the webhook and returns why it failed; "" when
// it was answered 2xx.
func (s *Server) post(hc *http.Client, h *webhook, u map[string]any) string {
	body, _ := json.Marshal(u)
	req, err := http.NewRequest(http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return "Bad webhook URL"
	}
	req.Header.Set("Content-Type", "application/json")
	if h.secret != "" {
		req.Header.Set(SecretHeader, h.secret)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "Connection refused"
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Sprintf("Wrong response from the webhook: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return ""
}

// confirm removes the update id from the queue: the webhook has it.
func (s *Server) confirm(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.updates[:0]
	for _, u := range s.updates {
		if u["update_id"].(int64) != id {
			kept = append(kept, u)
		}
	}
	s.updates = kept
	s.confirmed = max(s.confirmed, id)
}
