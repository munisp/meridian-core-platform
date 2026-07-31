package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// flakyProvider fails `fails` times before succeeding (records attempts).
type flakyProvider struct {
	name     string
	channels []string
	fails    int
	calls    int
}

func (f *flakyProvider) Name() string       { return f.name }
func (f *flakyProvider) Channels() []string { return f.channels }
func (f *flakyProvider) Send(m Message) SendResult {
	f.calls++
	if f.calls <= f.fails {
		return SendResult{Err: errors.New("provider down")}
	}
	return SendResult{ProviderID: f.name + "-1"}
}

func noSleep(time.Duration) {}

func TestFallbackChainAdvances(t *testing.T) {
	sms := &flakyProvider{name: "sms-prov", channels: []string{"sms"}, fails: 99} // always down
	email := &flakyProvider{name: "email-prov", channels: []string{"email", "ussd"}}
	o := newOrchestrator([]Provider{sms, email})
	o.sleep = noSleep
	msg := Message{ID: "m1", To: "t", TemplateID: "payment-reminder",
		Params: map[string]any{"amount": "1,500", "due_date": "2024-06-30", "reference": "r1"}}
	render := func(ch string) (string, error) { return renderTemplate(msg.TemplateID, ch, msg.Params) }
	final, receipts := o.run(msg, []string{"sms", "email", "agent"}, render)
	if final.Status != "sent" || final.Channel != "email" {
		t.Fatalf("expected email delivery after sms failure, got %v/%v", final.Status, final.Channel)
	}
	if !strings.Contains(final.Body, "₦1,500") {
		t.Fatalf("template not rendered: %q", final.Body)
	}
	// sms exhausted maxPerCh attempts, email succeeded on attempt 1
	var smsFails, emailSent int
	for _, rc := range receipts {
		if rc.Channel == "sms" && rc.Outcome == "failed" {
			smsFails++
		}
		if rc.Channel == "email" && rc.Outcome == "sent" {
			emailSent++
		}
	}
	if smsFails != o.maxPerCh || emailSent != 1 {
		t.Fatalf("bad attempt trace: sms=%d email=%d (%v)", smsFails, emailSent, receipts)
	}
}

func TestAgentTerminalFallback(t *testing.T) {
	dead := &flakyProvider{name: "dead", channels: []string{"sms", "ussd", "email"}, fails: 99}
	o := newOrchestrator([]Provider{dead})
	o.sleep = noSleep
	msg := Message{ID: "m2", To: "t", TemplateID: "filing-receipt",
		Params: map[string]any{"filing_id": "f1", "receipt": "rc1"}}
	render := func(ch string) (string, error) { return renderTemplate(msg.TemplateID, ch, msg.Params) }
	final, _ := o.run(msg, defaultChain, render)
	if final.Status != "queued_for_agent" || final.Channel != "agent" {
		t.Fatalf("terminal agent fallback not reached: %v/%v", final.Status, final.Channel)
	}
}

func TestRetryBackoffSameChannel(t *testing.T) {
	p := &flakyProvider{name: "flaky", channels: []string{"sms"}, fails: 2}
	o := newOrchestrator([]Provider{p})
	slept := 0
	o.sleep = func(time.Duration) { slept++ }
	msg := Message{ID: "m3", To: "t", TemplateID: "filing-receipt",
		Params: map[string]any{"filing_id": "f1", "receipt": "r"}}
	render := func(ch string) (string, error) { return renderTemplate(msg.TemplateID, ch, msg.Params) }
	final, receipts := o.run(msg, []string{"sms"}, render)
	if final.Status != "sent" {
		t.Fatalf("retry within channel should succeed, got %v", final.Status)
	}
	if p.calls != 3 || slept != 2 {
		t.Fatalf("expected 3 attempts with 2 backoffs, got %d/%d", p.calls, slept)
	}
	_ = receipts
}

func TestRenderTemplateUnknown(t *testing.T) {
	if _, err := renderTemplate("nope", "sms", nil); err == nil {
		t.Fatal("unknown template must error")
	}
	// channel variant fallback: ussd missing for filing-receipt -> any variant
	body, err := renderTemplate("filing-receipt", "ussd",
		map[string]any{"filing_id": "f1", "receipt": "r1"})
	if err != nil || !strings.Contains(body, "f1") {
		t.Fatalf("variant fallback failed: %v %q", err, body)
	}
}
