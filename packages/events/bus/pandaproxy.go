package bus

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
)

// PandaproxyBus talks to Redpanda's Pandaproxy REST API (no Go kafka client
// required). Publish: POST /topics/{topic} with JSON records. Subscribe:
// consumer instance + poll loop.
type Pandaproxy struct {
	base   string
	hc     *http.Client
	mu     sync.Mutex
	closed bool
	polls  []chan struct{}
}

// NewPandaproxy creates a bus targeting the given pandaproxy base URL.
func NewPandaproxy(baseURL string) *Pandaproxy {
	return &Pandaproxy{
		base: strings.TrimRight(baseURL, "/"),
		hc:   &http.Client{Timeout: 30 * time.Second},
	}
}

type ppRecord struct {
	Value string `json:"value"` // base64-encoded payload
}

type ppProduceRequest struct {
	Records []ppRecord `json:"records"`
}

// Publish posts the envelope JSON to pandaproxy.
func (p *Pandaproxy) Publish(ctx context.Context, topic string, env envelope.Envelope) error {
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	body, err := json.Marshal(ppProduceRequest{Records: []ppRecord{{Value: base64.StdEncoding.EncodeToString(raw)}}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/topics/"+topic, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/vnd.kafka.binary.v2+json")
	resp, err := p.hc.Do(req)
	if err != nil {
		return fmt.Errorf("pandaproxy publish: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pandaproxy publish: status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

type ppConsumerInstance struct {
	InstanceID string `json:"instance_id"`
	BaseURI    string `json:"base_uri"`
}

// Subscribe creates a pandaproxy consumer instance and polls records.
func (p *Pandaproxy) Subscribe(topic string, h Handler) (func(), error) {
	name := fmt.Sprintf("meridian-%d", time.Now().UnixNano())
	cfg := fmt.Sprintf(`{"name":%q,"format":"binary","auto.offset.reset":"earliest","auto.commit.enable":"true"}`, name)
	resp, err := p.hc.Post(p.base+"/consumers/meridian", "application/vnd.kafka.v2+json", strings.NewReader(cfg))
	if err != nil {
		return nil, fmt.Errorf("pandaproxy consumer create: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pandaproxy consumer create: status %d: %s", resp.StatusCode, string(b))
	}
	var inst ppConsumerInstance
	if err := json.NewDecoder(resp.Body).Decode(&inst); err != nil {
		return nil, err
	}
	subBody := fmt.Sprintf(`{"topics":[%q]}`, topic)
	req, _ := http.NewRequest(http.MethodPost, inst.BaseURI+"/subscription", strings.NewReader(subBody))
	req.Header.Set("Content-Type", "application/vnd.kafka.v2+json")
	sresp, err := p.hc.Do(req)
	if err != nil {
		return nil, err
	}
	sresp.Body.Close()
	if sresp.StatusCode >= 300 {
		return nil, fmt.Errorf("pandaproxy subscribe: status %d", sresp.StatusCode)
	}

	stop := make(chan struct{})
	p.mu.Lock()
	p.polls = append(p.polls, stop)
	p.mu.Unlock()

	go func() {
		type rec struct {
			Topic string `json:"topic"`
			Value string `json:"value"`
		}
		for {
			select {
			case <-stop:
				return
			default:
			}
			r, err := http.NewRequest(http.MethodGet, inst.BaseURI+"/records", nil)
			if err != nil {
				return
			}
			r.Header.Set("Accept", "application/vnd.kafka.binary.v2+json")
			rs, err := p.hc.Do(r)
			if err != nil {
				time.Sleep(2 * time.Second)
				continue
			}
			var recs []rec
			json.NewDecoder(rs.Body).Decode(&recs)
			rs.Body.Close()
			if len(recs) == 0 {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			for _, rr := range recs {
				raw, err := base64.StdEncoding.DecodeString(rr.Value)
				if err != nil {
					continue
				}
				var env envelope.Envelope
				if json.Unmarshal(raw, &env) == nil {
					// handler errors are dropped to DLQ topic by re-publish
					if herr := h(context.Background(), env); herr != nil {
						_ = p.Publish(context.Background(), envelope.DLQTopic(rr.Topic), env)
					}
				}
			}
		}
	}()

	cancel := func() {
		close(stop)
		r, _ := http.NewRequest(http.MethodDelete, inst.BaseURI, nil)
		r.Header.Set("Content-Type", "application/vnd.kafka.v2+json")
		if resp, err := p.hc.Do(r); err == nil {
			resp.Body.Close()
		}
	}
	return cancel, nil
}

// Close stops all poll loops.
func (p *Pandaproxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	for _, stop := range p.polls {
		defer func(s chan struct{}) { func() { defer func() { _ = recover() }(); close(s) }() }(stop)
	}
	return nil
}
