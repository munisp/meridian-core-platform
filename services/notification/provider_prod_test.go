package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
)

// QA-23: profile=prod without a real provider config must hard-fail
// (log.Fatal -> non-zero exit), never boot on the LogSimulator.
func TestProdWithoutProviderFatals(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		providersFromEnv(t.TempDir())
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestProdWithoutProviderFatals")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "PROFILE=prod", "NOTIFY_PROVIDER_URL=")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected non-zero exit (log.Fatal) for PROFILE=prod without NOTIFY_PROVIDER_URL")
	}
}

// Dev keeps the LogSimulator.
func TestDevKeepsLogSimulator(t *testing.T) {
	t.Setenv("PROFILE", "")
	t.Setenv("NOTIFY_PROVIDER_URL", "")
	ps := providersFromEnv(t.TempDir())
	if len(ps) != 1 {
		t.Fatalf("providers = %d, want 1", len(ps))
	}
	if _, ok := ps[0].(*LogSimulator); !ok {
		t.Fatalf("dev provider = %T, want *LogSimulator", ps[0])
	}
}

// NOTIFY_PROVIDER_URL selects the real webhook provider even in prod.
func TestWebhookProviderSelected(t *testing.T) {
	t.Setenv("PROFILE", "prod")
	t.Setenv("NOTIFY_PROVIDER_URL", "http://127.0.0.1:1")
	ps := providersFromEnv(t.TempDir())
	if _, ok := ps[0].(*WebhookProvider); !ok {
		t.Fatalf("provider = %T, want *WebhookProvider", ps[0])
	}
}

// The webhook provider actually POSTs the message and surfaces failures.
func TestWebhookProviderSend(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("X-Provider-Id", "gw-1")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	p := NewWebhookProvider(srv.URL)
	res := p.Send(Message{ID: "m1", Channel: "sms", To: "+234", Body: "hi"})
	if res.Err != nil || res.ProviderID != "gw-1" || hits != 1 {
		t.Fatalf("send: %+v hits=%d", res, hits)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer bad.Close()
	if res := NewWebhookProvider(bad.URL).Send(Message{ID: "m2"}); res.Err == nil {
		t.Fatal("provider 5xx must surface as error")
	}
}
