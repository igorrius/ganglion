// Command webhookspike is the spike of task 6.6 (ADR-0078): it measures what
// the Telegram webhook mode will meet on a host with a public address. It
// registers a webhook for a test bot, logs every delivery attempt, and
// answers in the chat with what it measured.
//
// Messages to the bot:
//
//	/down N   answer 503 to every delivery for N seconds, then report when
//	          Telegram tried again
//	/info     getWebhookInfo and the largest update seen
//	/bye      deleteWebhook; the bot polls again elsewhere
//
// Environment: TELEGRAM_BOT_TOKEN; PUBLIC_URL or RAILWAY_PUBLIC_DOMAIN;
// PORT (default 8080).
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	secretHeader = "X-Telegram-Bot-Api-Secret-Token"
	hookPath     = "/telegram/webhook"
	maxBody      = 4 << 20
)

type spike struct {
	token, publicURL, secret, nonce string
	started                         time.Time
	hc                              *http.Client

	mu        sync.Mutex
	downUntil time.Time
	attempts  map[int64][]time.Time // refused deliveries by update_id
	maxSize   int
	hookSet   time.Time
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Date int64  `json:"date"`
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	s := &spike{
		token:    os.Getenv("TELEGRAM_BOT_TOKEN"),
		started:  time.Now(),
		hc:       &http.Client{Timeout: 20 * time.Second},
		attempts: map[int64][]time.Time{},
	}
	if s.token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is not set")
	}
	log.Printf("env: PORT=%q RAILWAY_PUBLIC_DOMAIN=%q PUBLIC_URL=%q",
		os.Getenv("PORT"), os.Getenv("RAILWAY_PUBLIC_DOMAIN"), os.Getenv("PUBLIC_URL"))
	s.publicURL = strings.TrimRight(os.Getenv("PUBLIC_URL"), "/")
	if d := os.Getenv("RAILWAY_PUBLIC_DOMAIN"); s.publicURL == "" && d != "" {
		s.publicURL = "https://" + d
	}
	mac := hmac.New(sha256.New, []byte(s.token))
	mac.Write([]byte("ganglion telegram webhook v1"))
	s.secret = hex.EncodeToString(mac.Sum(nil))
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	s.nonce = hex.EncodeToString(nonce)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, s.nonce) })
	mux.HandleFunc(hookPath, s.hook)
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { log.Fatal(srv.ListenAndServe()) }()
	log.Printf("listening on :%s", port)

	if s.publicURL == "" {
		log.Print("no public address: nothing registered; set PUBLIC_URL or generate a domain")
		select {}
	}
	ctx := context.Background()
	s.logInfo(ctx, "before setWebhook")
	if !s.selfCheck() {
		log.Print("self-check failed: the webhook is NOT registered")
		select {}
	}
	var ok bool
	err := s.api(ctx, "setWebhook", map[string]any{
		"url": s.publicURL + hookPath, "secret_token": s.secret,
		"max_connections": 1, "allowed_updates": []string{"message", "callback_query"},
	}, &ok)
	s.mu.Lock()
	s.hookSet = time.Now()
	s.mu.Unlock()
	log.Printf("setWebhook: ok=%v err=%v, %.1fs after the process start", ok, err, time.Since(s.started).Seconds())
	select {}
}

// selfCheck asks the public address for this process's nonce, a few times:
// a fresh domain may take a moment to route.
func (s *spike) selfCheck() bool {
	for i := 1; i <= 10; i++ {
		t := time.Now()
		resp, err := s.hc.Get(s.publicURL + "/healthz")
		if err != nil {
			log.Printf("self-check %d: %v", i, err)
		} else {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			_ = resp.Body.Close()
			match := string(b) == s.nonce
			log.Printf("self-check %d: status=%d match=%v in %s", i, resp.StatusCode, match, time.Since(t).Round(time.Millisecond))
			if match {
				return true
			}
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

func (s *spike) hook(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(secretHeader)), []byte(s.secret)) != 1 {
		log.Printf("hook: wrong or missing secret from xff=%q", r.Header.Get("X-Forwarded-For"))
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		log.Printf("hook: body: %v", err)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	var u update
	if err := json.Unmarshal(body, &u); err != nil {
		log.Printf("hook: not an update (%d bytes): %v", len(body), err)
		return
	}
	s.mu.Lock()
	if len(body) > s.maxSize {
		s.maxSize = len(body)
	}
	down := now.Before(s.downUntil)
	if down {
		s.attempts[u.UpdateID] = append(s.attempts[u.UpdateID], now)
	}
	refused := s.attempts[u.UpdateID]
	if !down {
		delete(s.attempts, u.UpdateID)
	}
	hookSet := s.hookSet
	s.mu.Unlock()

	log.Printf("hook: update=%d bytes=%d down=%v xff=%q real_ip=%q proto=%q",
		u.UpdateID, len(body), down, r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Real-Ip"), r.Header.Get("X-Forwarded-Proto"))
	if down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if u.Message == nil {
		return
	}
	sent := time.Unix(u.Message.Date, 0)
	var b strings.Builder
	fmt.Fprintf(&b, "update %d, %d bytes, arrived %.1fs after it was sent", u.UpdateID, len(body), now.Sub(sent).Seconds())
	if sent.Before(s.started) {
		fmt.Fprintf(&b, "\nsent before this start: %.1fs after the process start, %.1fs after setWebhook",
			now.Sub(s.started).Seconds(), now.Sub(hookSet).Seconds())
	}
	if len(refused) > 0 {
		b.WriteString("\nrefused with 503 at")
		for _, t := range refused {
			fmt.Fprintf(&b, " +%.0fs", t.Sub(sent).Seconds())
		}
	}
	log.Print(strings.ReplaceAll(b.String(), "\n", "; "))
	chat := u.Message.Chat.ID
	ctx := r.Context()
	switch f := strings.Fields(u.Message.Text); {
	case len(f) == 2 && f[0] == "/down":
		n, _ := strconv.Atoi(f[1])
		s.mu.Lock()
		s.downUntil = now.Add(time.Duration(n) * time.Second)
		s.mu.Unlock()
		fmt.Fprintf(&b, "\n503 for the next %d s: send a message now", n)
	case len(f) == 1 && f[0] == "/info":
		b.WriteString("\n" + s.logInfo(ctx, "/info"))
	case len(f) == 1 && f[0] == "/bye":
		var ok bool
		err := s.api(ctx, "deleteWebhook", map[string]any{}, &ok)
		fmt.Fprintf(&b, "\ndeleteWebhook: ok=%v err=%v", ok, err)
	}
	s.say(ctx, chat, b.String())
}

// logInfo logs getWebhookInfo, without the address's secret parts (there are
// none: the secret travels in a header), and returns the line.
func (s *spike) logInfo(ctx context.Context, when string) string {
	var info map[string]any
	err := s.api(ctx, "getWebhookInfo", map[string]any{}, &info)
	s.mu.Lock()
	maxSize := s.maxSize
	s.mu.Unlock()
	line := fmt.Sprintf("getWebhookInfo (%s): %v err=%v; largest update %d bytes", when, info, err, maxSize)
	log.Print(line)
	return line
}

func (s *spike) say(ctx context.Context, chat int64, text string) {
	var out json.RawMessage
	if err := s.api(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": text}, &out); err != nil {
		log.Printf("sendMessage: %v", err)
	}
}

// api calls a Bot API method. Errors never carry the address, which holds
// the token.
func (s *spike) api(ctx context.Context, method string, params, out any) error {
	body, _ := json.Marshal(params)
	req, _ := http.NewRequestWithContext(context.WithoutCancel(ctx), http.MethodPost,
		"https://api.telegram.org/bot"+s.token+"/"+method, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s: request failed", method)
	}
	defer func() { _ = resp.Body.Close() }()
	var env struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if !env.OK {
		return fmt.Errorf("%s: %d %s", method, resp.StatusCode, env.Description)
	}
	return json.Unmarshal(env.Result, out)
}
