// notification — SMS/USSD/email/push dispatch (SPEC 2).
// Providers sit behind the Provider interface; the dev default is a durable
// log simulator (writes to DATA_DIR/notifications.log).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/envelope"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/packages/events/store"
)

const (
	service = "notification"
	version = "0.1.0"
)

// Message is one outbound notification.
type Message struct {
	ID             string         `json:"id"`
	Channel        string         `json:"channel"` // sms|ussd|email|push
	To             string         `json:"to"`
	Body           string         `json:"body"`
	TemplateID     string         `json:"template_id,omitempty"`
	Params         map[string]any `json:"params,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	Status         string         `json:"status"` // queued|sent|delivered|failed
	Provider       string         `json:"provider"`
	ProviderID     string         `json:"provider_id,omitempty"`
	Error          string         `json:"error,omitempty"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

// SendResult is what a provider returns.
type SendResult struct {
	ProviderID string
	Err        error
}

// Provider abstracts SMS/USSD/email/push rails (SPEC: providers behind
// interface + log simulator).
type Provider interface {
	Name() string
	Channels() []string
	Send(m Message) SendResult
}

// LogSimulator is the dev provider: "sends" by appending to a log file.
type LogSimulator struct {
	path string
	mu   sync.Mutex
	seq  int
}

// NewLogSimulator creates the simulator writing to path.
func NewLogSimulator(path string) *LogSimulator { return &LogSimulator{path: path} }

// Name implements Provider.
func (l *LogSimulator) Name() string { return "log-simulator [simulated]" }

// Channels implements Provider.
func (l *LogSimulator) Channels() []string { return []string{"sms", "ussd", "email", "push"} }

// Send implements Provider.
func (l *LogSimulator) Send(m Message) SendResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	line := fmt.Sprintf("%s [%s] to=%s body=%q\n", time.Now().UTC().Format(time.RFC3339), m.Channel, m.To, m.Body)
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return SendResult{Err: err}
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		return SendResult{Err: err}
	}
	return SendResult{ProviderID: fmt.Sprintf("logsim-%d-%d", time.Now().Unix(), l.seq)}
}

// WebhookProvider is the real dispatch path: POSTs each message as JSON to
// the configured provider gateway (NOTIFY_PROVIDER_URL), which fronts the
// actual SMS/email/push rails. A bounded http.Client is mandatory (QA-23).
type WebhookProvider struct {
	url    string
	client *http.Client
}

// NewWebhookProvider creates the real provider pointed at url.
func NewWebhookProvider(url string) *WebhookProvider {
	return &WebhookProvider{url: url, client: &http.Client{Timeout: 10 * time.Second}}
}

// Name implements Provider.
func (w *WebhookProvider) Name() string { return "webhook-gateway" }

// Channels implements Provider.
func (w *WebhookProvider) Channels() []string { return []string{"sms", "ussd", "email", "push"} }

// Send implements Provider.
func (w *WebhookProvider) Send(m Message) SendResult {
	body, err := json.Marshal(m)
	if err != nil {
		return SendResult{Err: err}
	}
	resp, err := w.client.Post(w.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return SendResult{Err: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return SendResult{Err: fmt.Errorf("provider gateway returned %s", resp.Status)}
	}
	return SendResult{ProviderID: resp.Header.Get("X-Provider-Id")}
}

// providersFromEnv wires the provider set (QA-23, fail-closed in prod):
//   - NOTIFY_PROVIDER_URL set -> the real webhook provider (prod contract)
//   - PROFILE=prod without NOTIFY_PROVIDER_URL -> hard fatal: the service
//     must never "send" regulated notifications to a log file in prod
//   - otherwise (dev) -> LogSimulator
func providersFromEnv(dir string) []Provider {
	if u := os.Getenv("NOTIFY_PROVIDER_URL"); u != "" {
		log.Printf("profile=prod component=notification-provider webhook-gateway url=%s", u)
		return []Provider{NewWebhookProvider(u)}
	}
	if os.Getenv("PROFILE") == "prod" || os.Getenv("PROFILE") == "production" {
		log.Fatal("PROFILE=prod FATAL: NOTIFY_PROVIDER_URL is required (refusing to boot with the LogSimulator as sole provider)")
	}
	log.Printf("profile=dev component=notification-provider (log simulator)")
	return []Provider{NewLogSimulator(filepath.Join(dir, "notifications.log"))}
}

type server struct {
	st        store.DocStore
	providers []Provider
	orch      *orchestrator // I4 fallback chain
}

func (s *server) providerFor(channel string) Provider {
	for _, p := range s.providers {
		for _, c := range p.Channels() {
			if c == channel {
				return p
			}
		}
	}
	return nil
}

func main() {
	dir := httpx.Env("DATA_DIR", "./data")
	st, err := store.OpenFromEnv(dir)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	s := &server{st: st, providers: providersFromEnv(dir)}
	s.orch = newOrchestrator(s.providers)

	mux := http.NewServeMux()
	httpx.RegisterStandard(mux, service, version, nil)
	mux.HandleFunc("POST /v1/send", s.send)
	mux.HandleFunc("POST /v1/notify", s.notify) // I4: fallback chain
	mux.HandleFunc("GET /v1/receipts/{id}", s.receipts)
	mux.HandleFunc("GET /v1/status/{id}", s.status)
	mux.HandleFunc("GET /v1/providers", func(w http.ResponseWriter, r *http.Request) {
		names := []string{}
		for _, p := range s.providers {
			names = append(names, p.Name())
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"providers": names})
	})

	addr := ":" + httpx.Port("8006")
	log.Printf("%s %s", service, version)
	log.Fatal(httpx.ListenAndServe(addr, auth.Middleware(mux)))
}

type sendReq struct {
	Channel        string         `json:"channel"`
	To             string         `json:"to"`
	Body           string         `json:"body"`
	TemplateID     string         `json:"template_id,omitempty"`
	Params         map[string]any `json:"params,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
}

var channels = map[string]bool{"sms": true, "ussd": true, "email": true, "push": true}

func (s *server) send(w http.ResponseWriter, r *http.Request) {
	var req sendReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	if !channels[req.Channel] {
		httpx.BadRequest(w, "channel must be sms|ussd|email|push")
		return
	}
	if req.To == "" {
		httpx.BadRequest(w, "to is required")
		return
	}
	if req.Body == "" && req.TemplateID == "" {
		httpx.BadRequest(w, "body or template_id required")
		return
	}
	// idempotency: same key returns the original message. B4-14: fail-closed —
	// if the lookup errors for any reason OTHER than not-found (store down,
	// corrupt doc), refuse with 503 rather than risk a double-send of a
	// regulated notification.
	if req.IdempotencyKey != "" {
		var existing Message
		err := s.st.Get("by_key", req.IdempotencyKey, &existing)
		switch {
		case err == nil:
			httpx.JSON(w, http.StatusOK, map[string]any{"message": existing, "note": "idempotent replay"})
			return
		case !errors.Is(err, store.ErrNotFound):
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]any{
				"type": "about:blank", "title": "idempotency_store_unavailable", "status": 503,
				"detail": "idempotency lookup failed; refusing to risk duplicate send",
			})
			return
		}
	}
	p := s.providerFor(req.Channel)
	if p == nil {
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]any{
			"type": "about:blank", "title": "no_provider", "status": 503,
			"detail": "no provider for channel " + req.Channel,
		})
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	msg := Message{
		ID:             "msg-" + envelope.NewULID(),
		Channel:        req.Channel,
		To:             req.To,
		Body:           req.Body,
		TemplateID:     req.TemplateID,
		Params:         req.Params,
		IdempotencyKey: req.IdempotencyKey,
		Status:         "queued",
		Provider:       p.Name(),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	res := p.Send(msg)
	msg.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if res.Err != nil {
		msg.Status = "failed"
		msg.Error = res.Err.Error()
	} else {
		msg.Status = "sent" // log simulator: delivered receipts out of scope in dev
		msg.ProviderID = res.ProviderID
	}
	if err := s.st.Put("messages", msg.ID, msg); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	if req.IdempotencyKey != "" {
		if err := s.st.Put("by_key", req.IdempotencyKey, msg); err != nil {
			httpx.Internal(w, "%v", err)
			return
		}
	}
	status := http.StatusAccepted
	if msg.Status == "failed" {
		status = http.StatusBadGateway
	}
	httpx.JSON(w, status, map[string]any{"message": msg})
}

// notify — I4: POST /v1/notify {to, template_id, params, chain?,
// idempotency_key} walks the fallback chain (sms->ussd->email->agent) with
// per-channel templates, retry/backoff, and persisted delivery receipts.
func (s *server) notify(w http.ResponseWriter, r *http.Request) {
	var req notifyReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	if req.To == "" || req.TemplateID == "" {
		httpx.BadRequest(w, "to and template_id are required")
		return
	}
	// B4-14: same fail-closed idempotency semantics as /v1/send.
	if req.IdempotencyKey != "" {
		var existing Message
		err := s.st.Get("by_key", "notify:"+req.IdempotencyKey, &existing)
		switch {
		case err == nil:
			httpx.JSON(w, http.StatusOK, map[string]any{"message": existing, "note": "idempotent replay"})
			return
		case !errors.Is(err, store.ErrNotFound):
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]any{
				"type": "about:blank", "title": "idempotency_store_unavailable", "status": 503,
				"detail": "idempotency lookup failed; refusing to risk duplicate send",
			})
			return
		}
	}
	chain := req.Chain
	if len(chain) == 0 {
		chain = defaultChain
	}
	for _, ch := range chain {
		if !channels[ch] && ch != "agent" {
			httpx.BadRequest(w, "chain entries must be sms|ussd|email|push|agent")
			return
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	msg := Message{
		ID: "msg-" + envelope.NewULID(), To: req.To, TemplateID: req.TemplateID,
		Params: req.Params, IdempotencyKey: req.IdempotencyKey,
		Status: "queued", CreatedAt: now, UpdatedAt: now,
	}
	render := func(channel string) (string, error) {
		return renderTemplate(req.TemplateID, channel, req.Params)
	}
	msg, receipts := s.orch.run(msg, chain, render)
	if err := s.st.Put("messages", msg.ID, msg); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	for _, rc := range receipts {
		rc.ID = "drcpt-" + envelope.NewULID()
		if err := s.st.Put("receipts", rc.ID, rc); err != nil {
			httpx.Internal(w, "%v", err)
			return
		}
	}
	if req.IdempotencyKey != "" {
		_ = s.st.Put("by_key", "notify:"+req.IdempotencyKey, msg)
	}
	status := http.StatusAccepted
	if msg.Status == "failed" {
		status = http.StatusBadGateway
	}
	httpx.JSON(w, status, map[string]any{"message": msg, "attempts": len(receipts), "chain": chain})
}

// receipts — I4: GET /v1/receipts/{message_id} lists delivery receipts.
func (s *server) receipts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var all []DeliveryReceipt
	if err := s.st.ListInto("receipts", &all); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	out := []DeliveryReceipt{}
	for _, rc := range all {
		if rc.MessageID == id {
			out = append(out, rc)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"receipts": out, "count": len(out)})
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	var msg Message
	if err := s.st.Get("messages", r.PathValue("id"), &msg); err != nil {
		httpx.NotFound(w, "message %s", r.PathValue("id"))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"message": msg})
}
