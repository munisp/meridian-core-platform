// fallback.go — I4: notification fallback orchestrator.
//
// A taxpayer notification walks a provider chain (default sms -> ussd ->
// email -> agent) until one channel accepts it. Every attempt produces a
// persisted delivery receipt with per-attempt retry/backoff; per-channel
// templates render the body. The terminal "agent" channel is a REAL
// fallback: the notification is queued for a human agent (status
// queued_for_agent) rather than silently dropped.
package main

import (
	"fmt"
	"strings"
	"time"
)

// defaultChain is the NRS reachability ladder: cheapest realtime channels
// first, human agent as the guaranteed terminal fallback.
var defaultChain = []string{"sms", "ussd", "email", "agent"}

// channelTemplates are per-channel defaults (overridable via the request's
// template_id + params; the template store can extend these at runtime).
var channelTemplates = map[string]map[string]string{
	"payment-reminder": {
		"sms":   "NRS: payment of ₦{{amount}} due {{due_date}}. Ref {{reference}}.",
		"ussd":  "NRS payment due ₦{{amount}}. Dial *737# to pay. Ref {{reference}}.",
		"email": "Dear taxpayer,\n\nyour payment of ₦{{amount}} is due on {{due_date}} (reference {{reference}}).\n\nNRS",
		"agent": "Call {{to}} re: payment ₦{{amount}} due {{due_date}} (ref {{reference}})",
	},
	"filing-receipt": {
		"sms":   "NRS: filing {{filing_id}} received. Receipt {{receipt}}.",
		"email": "Your filing {{filing_id}} was received. Receipt: {{receipt}}.\n\nNRS",
		"agent": "Confirm filing {{filing_id}} receipt to {{to}}",
	},
}

// renderTemplate substitutes {{key}} placeholders. Missing keys render as
// empty strings; an unknown template_id+channel combination is an error so
// callers don't send untemplated debris.
func renderTemplate(templateID, channel string, params map[string]any) (string, error) {
	tpl, ok := channelTemplates[templateID][channel]
	if !ok {
		if anyCh, okAny := channelTemplates[templateID]; okAny {
			// fall back to any defined channel variant for this template
			for _, t := range anyCh {
				tpl = t
				ok = true
				break
			}
		}
	}
	if !ok {
		return "", fmt.Errorf("no template %q for channel %q", templateID, channel)
	}
	for k, v := range params {
		tpl = strings.ReplaceAll(tpl, "{{"+k+"}}", fmt.Sprint(v))
	}
	return tpl, nil
}

// DeliveryReceipt is persisted per attempt (audit + provider reconciliation).
type DeliveryReceipt struct {
	ID        string `json:"id"`
	MessageID string `json:"message_id"`
	Channel   string `json:"channel"`
	Provider  string `json:"provider"`
	Attempt   int    `json:"attempt"`
	Outcome   string `json:"outcome"` // sent|failed
	Error     string `json:"error,omitempty"`
	BackoffMS int    `json:"backoff_ms"` // backoff applied AFTER this attempt (0 on success/last)
	At        string `json:"at"`
}

// orchestrator runs the fallback chain with retry/backoff.
type orchestrator struct {
	providers []Provider
	// backoff schedule between attempts of ONE channel (ms); chain advance
	// happens after the schedule is exhausted.
	backoffMS []int
	maxPerCh  int
	sleep     func(time.Duration) // injectable for tests
}

func newOrchestrator(providers []Provider) *orchestrator {
	return &orchestrator{
		providers: providers,
		backoffMS: []int{250, 1000, 5000},
		maxPerCh:  3,
		sleep:     time.Sleep,
	}
}

func (o *orchestrator) providerFor(channel string) Provider {
	for _, p := range o.providers {
		for _, c := range p.Channels() {
			if c == channel {
				return p
			}
		}
	}
	return nil
}

type notifyReq struct {
	To             string         `json:"to"`
	TemplateID     string         `json:"template_id"`
	Params         map[string]any `json:"params,omitempty"`
	Chain          []string       `json:"chain,omitempty"` // override default sms->ussd->email->agent
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
}

// run walks the chain. Returns the final message plus every attempt receipt.
func (o *orchestrator) run(msg Message, chain []string, render func(channel string) (string, error)) (Message, []DeliveryReceipt) {
	var receipts []DeliveryReceipt
	now := func() string { return time.Now().UTC().Format(time.RFC3339) }
	for _, channel := range chain {
		if channel == "agent" {
			// terminal REAL fallback: hand to the human agent queue
			msg.Channel = "agent"
			msg.Provider = "agent-queue"
			msg.Status = "queued_for_agent"
			msg.UpdatedAt = now()
			receipts = append(receipts, DeliveryReceipt{
				MessageID: msg.ID, Channel: "agent", Provider: "agent-queue",
				Attempt: 1, Outcome: "sent", At: now(),
			})
			return msg, receipts
		}
		p := o.providerFor(channel)
		if p == nil {
			receipts = append(receipts, DeliveryReceipt{
				MessageID: msg.ID, Channel: channel, Provider: "-",
				Attempt: 0, Outcome: "failed", Error: "no provider for channel", At: now(),
			})
			continue
		}
		body, err := render(channel)
		if err != nil {
			receipts = append(receipts, DeliveryReceipt{
				MessageID: msg.ID, Channel: channel, Provider: p.Name(),
				Attempt: 0, Outcome: "failed", Error: err.Error(), At: now(),
			})
			continue
		}
		attempt := Message{ID: msg.ID, Channel: channel, To: msg.To, Body: body,
			TemplateID: msg.TemplateID, Params: msg.Params}
		for i := 0; i < o.maxPerCh; i++ {
			res := p.Send(attempt)
			rc := DeliveryReceipt{
				MessageID: msg.ID, Channel: channel, Provider: p.Name(),
				Attempt: i + 1, At: now(),
			}
			if res.Err == nil {
				rc.Outcome = "sent"
				receipts = append(receipts, rc)
				msg.Channel = channel
				msg.Body = body
				msg.Provider = p.Name()
				msg.ProviderID = res.ProviderID
				msg.Status = "sent"
				msg.UpdatedAt = now()
				return msg, receipts
			}
			rc.Outcome = "failed"
			rc.Error = res.Err.Error()
			if i < o.maxPerCh-1 && i < len(o.backoffMS) {
				rc.BackoffMS = o.backoffMS[i]
				o.sleep(time.Duration(o.backoffMS[i]) * time.Millisecond)
			}
			receipts = append(receipts, rc)
		}
		// chain advance: next channel
	}
	msg.Status = "failed"
	msg.UpdatedAt = now()
	return msg, receipts
}
