// Package bus provides the event bus interface used by all services.
// Default dev mode is an in-process bus (EVENT_BUS=inproc, the default).
// EVENT_BUS=kafka uses Redpanda Pandaproxy REST (KAFKA_PROXY_URL) so no
// external Go client dependency is required; compose enables pandaproxy.
package bus

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
)

// Handler processes one event. Returning an error routes the event to the DLQ
// (inproc) or leaves it uncommitted (kafka mode).
type Handler func(ctx context.Context, env envelope.Envelope) error

// Bus is the publish/subscribe abstraction over Redpanda/Kafka.
type Bus interface {
	Publish(ctx context.Context, topic string, env envelope.Envelope) error
	Subscribe(topic string, h Handler) (cancel func(), err error)
	Close() error
}

// NewFromEnv selects the bus implementation from the environment:
//
//	EVENT_BUS=inproc (default)  -> InprocBus
//	EVENT_BUS=kafka             -> PandaproxyBus using KAFKA_PROXY_URL
//	                               (default http://localhost:8082)
func NewFromEnv() Bus {
	var b Bus
	// Prod profile: KAFKA_BROKERS set -> real franz-go client (HARDENING H3).
	if os.Getenv("KAFKA_BROKERS") != "" {
		if kb, err := NewKafkaFromEnv(); err != nil {
			log.Printf("profile=dev component=bus kafka init failed (%v); inproc fallback", err)
		} else {
			log.Printf("profile=prod component=bus brokers=%s", os.Getenv("KAFKA_BROKERS"))
			return maybeValidating(kb)
		}
	}
	switch os.Getenv("EVENT_BUS") {
	case "", "inproc":
		log.Printf("profile=dev component=bus inproc")
		b = NewInproc()
	case "kafka":
		proxy := os.Getenv("KAFKA_PROXY_URL")
		if proxy == "" {
			proxy = "http://localhost:8082"
		}
		log.Printf("bus: EVENT_BUS=kafka via Redpanda pandaproxy at %s (KAFKA_BROKERS=%s)", proxy, os.Getenv("KAFKA_BROKERS"))
		b = NewPandaproxy(proxy)
	default:
		log.Printf("bus: unknown EVENT_BUS=%q, falling back to inproc", os.Getenv("EVENT_BUS"))
		b = NewInproc()
	}
	return maybeValidating(b)
}

// maybeValidating wraps the bus with ValidateBeforePublish when a hook is
// installed (see SetPublishValidator).
func maybeValidating(b Bus) Bus {
	validatorMu.RLock()
	defer validatorMu.RUnlock()
	if publishValidator == nil {
		return b
	}
	return NewValidating(b)
}

// --- In-process bus (dev default, also the outbox relay target in dev) ---

// InprocBus is a goroutine-safe in-process pub/sub bus implementing the same
// semantics as the Kafka bus: per-topic subscriber groups, DLQ on handler
// error, and retained history for tests.
type InprocBus struct {
	mu     sync.RWMutex
	subs   map[string][]Handler
	dlq    map[string][]envelope.Envelope
	closed bool
}

// NewInproc creates an empty in-process bus.
func NewInproc() *InprocBus {
	return &InprocBus{subs: map[string][]Handler{}, dlq: map[string][]envelope.Envelope{}}
}

// Publish delivers the event to all current subscribers of the topic.
// Handler failures are routed to topic+".dlq" (SPEC 1.2).
func (b *InprocBus) Publish(ctx context.Context, topic string, env envelope.Envelope) error {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return errors.New("bus closed")
	}
	handlers := append([]Handler(nil), b.subs[topic]...)
	b.mu.RUnlock()
	for _, h := range handlers {
		if err := h(ctx, env); err != nil {
			b.mu.Lock()
			b.dlq[envelope.DLQTopic(topic)] = append(b.dlq[envelope.DLQTopic(topic)], env)
			b.mu.Unlock()
		}
	}
	return nil
}

// Subscribe registers a handler for a topic. Cancel unregisters it.
func (b *InprocBus) Subscribe(topic string, h Handler) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errors.New("bus closed")
	}
	b.subs[topic] = append(b.subs[topic], h)
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		hs := b.subs[topic]
		for i, hh := range hs {
			if fmt.Sprintf("%p", hh) == fmt.Sprintf("%p", h) {
				b.subs[topic] = append(hs[:i], hs[i+1:]...)
				return
			}
		}
	}, nil
}

// DLQ returns dead-lettered events for a base topic (test/diagnostic use).
func (b *InprocBus) DLQ(topic string) []envelope.Envelope {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]envelope.Envelope(nil), b.dlq[envelope.DLQTopic(topic)]...)
}

// Close shuts the bus down.
func (b *InprocBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}
