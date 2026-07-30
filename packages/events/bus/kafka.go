// Kafka/Redpanda bus via franz-go (HARDENING H3). Selected when
// KAFKA_BROKERS is set (comma-separated host:port list); the embedded
// inproc bus remains the dev fallback. Topics are identical to the inproc
// bus (SPEC 1.2); consumer offsets are only committed after the handler
// succeeds, so handler errors leave the record uncommitted (matching the
// "leaves it uncommitted" semantics of the Bus contract).
package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
)

// KafkaBus is a franz-go backed Bus (Redpanda/Kafka).
type KafkaBus struct {
	brokers []string
	group   string

	mu     sync.Mutex
	prod   *kgo.Client
	subs   []subCancel
	closed bool
}

type subCancel struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewKafka creates a bus connecting to the given brokers. group is the
// Kafka consumer group (KAFKA_GROUP, default "meridian-core").
func NewKafka(brokers []string, group string) (*KafkaBus, error) {
	if len(brokers) == 0 {
		return nil, errors.New("kafka: at least one broker required")
	}
	if group == "" {
		group = "meridian-core"
	}
	prod, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerLinger(5*time.Millisecond),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka: producer client: %w", err)
	}
	return &KafkaBus{brokers: brokers, group: group, prod: prod}, nil
}

// Publish produces the envelope as a JSON record (key = envelope id).
func (b *KafkaBus) Publish(ctx context.Context, topic string, env envelope.Envelope) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("bus closed")
	}
	b.mu.Unlock()
	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}
	rec := &kgo.Record{Topic: topic, Key: []byte(env.ID), Value: payload}
	res := b.prod.ProduceSync(ctx, rec)
	if err := res.FirstErr(); err != nil {
		return fmt.Errorf("kafka: produce %s: %w", topic, err)
	}
	return nil
}

// Subscribe registers a handler in the bus consumer group for topic.
// Records are committed only after the handler returns nil; on error the
// offset stays uncommitted and the record is redelivered.
func (b *KafkaBus) Subscribe(topic string, h Handler) (func(), error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, errors.New("bus closed")
	}
	b.mu.Unlock()
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokersClean(b.brokers)...),
		kgo.ConsumerGroup(b.group),
		kgo.ConsumeTopics(topic),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka: consumer client: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cl.Close()
		for {
			fetches := cl.PollFetches(ctx)
			if ctx.Err() != nil {
				return
			}
			if errs := fetches.Errors(); len(errs) > 0 {
				for _, e := range errs {
					log.Printf("bus kafka: fetch %v", e)
				}
				continue
			}
			var commit []*kgo.Record
			fetches.EachRecord(func(rec *kgo.Record) {
				var env envelope.Envelope
				if err := json.Unmarshal(rec.Value, &env); err != nil {
					log.Printf("bus kafka: undecodable record on %s (offset %d): %v", rec.Topic, rec.Offset, err)
					commit = append(commit, rec) // poison record: skip but commit past it
					return
				}
				if err := h(ctx, env); err != nil {
					log.Printf("bus kafka: handler error on %s (offset %d): %v — offset left uncommitted", rec.Topic, rec.Offset, err)
					return
				}
				commit = append(commit, rec)
			})
			if len(commit) > 0 {
				if err := cl.CommitRecords(ctx, commit...); err != nil && ctx.Err() == nil {
					log.Printf("bus kafka: commit: %v", err)
				}
			}
		}
	}()
	b.mu.Lock()
	b.subs = append(b.subs, subCancel{cancel: cancel, done: done})
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}, nil
}

func brokersClean(in []string) []string {
	out := make([]string, 0, len(in))
	for _, b := range in {
		if s := strings.TrimSpace(b); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Close shuts the bus down (producer + all subscription consumers).
func (b *KafkaBus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	subs := append([]subCancel(nil), b.subs...)
	prod := b.prod
	b.mu.Unlock()
	for _, s := range subs {
		s.cancel()
		<-s.done
	}
	if prod != nil {
		prod.Close()
	}
	return nil
}

// NewKafkaFromEnv builds a KafkaBus from KAFKA_BROKERS / KAFKA_GROUP.
func NewKafkaFromEnv() (*KafkaBus, error) {
	brokers := brokersClean(strings.Split(httpx.Env("KAFKA_BROKERS", ""), ","))
	return NewKafka(brokers, httpx.Env("KAFKA_GROUP", ""))
}
