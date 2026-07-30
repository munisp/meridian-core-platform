// search-indexer — outbox->index service (SPEC 2). Ingests event envelopes
// (bus subscription to INDEX_TOPICS, POST /v1/index, OUTBOX_WATCH_DIR poll),
// indexes to OpenSearch when OPENSEARCH_URL is set (real bulk API) and to
// the embedded local index always (dev fallback searched by /v1/search).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/bus"
	"github.com/munisp/meridian-core-platform/packages/events/envelope"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/packages/events/store"
	"github.com/munisp/meridian-core-platform/services/search-indexer/internal/index"
)

const (
	service = "search-indexer"
	version = "0.1.0"
)

type server struct {
	ix         *index.Index
	opensearch string
	hc         *http.Client
}

func main() {
	dir := httpx.Env("DATA_DIR", "./data")
	st, err := store.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{
		ix:         index.New(st),
		opensearch: strings.TrimRight(os.Getenv("OPENSEARCH_URL"), "/"),
		hc:         &http.Client{Timeout: 5 * time.Second},
	}

	// bus subscription (inproc: same-process only; kafka: real)
	b := bus.NewFromEnv()
	defer b.Close()
	topics := httpx.Env("INDEX_TOPICS", "nrs.rulepacks.published.v1,nrs.ledger.transfers.v1")
	for _, topic := range strings.Split(topics, ",") {
		topic = strings.TrimSpace(topic)
		if topic == "" {
			continue
		}
		t := topic
		if _, err := b.Subscribe(t, func(ctx context.Context, env envelope.Envelope) error {
			s.ingest(t, env)
			return nil
		}); err != nil {
			log.Printf("subscribe %s: %v", t, err)
		}
	}
	// outbox dir watch: pick up other dev services' outbox files
	if watch := os.Getenv("OUTBOX_WATCH_DIR"); watch != "" {
		go s.watchOutboxDirs(watch)
	}

	mux := http.NewServeMux()
	httpx.RegisterStandard(mux, service, version, nil)
	mux.HandleFunc("POST /v1/index", s.indexDoc)
	mux.HandleFunc("GET /v1/search", s.search)
	mux.HandleFunc("GET /v1/stats", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]any{
			"docs": s.ix.Count(), "opensearch": s.opensearch != "",
		})
	})

	addr := ":" + httpx.Port("8008")
	log.Printf("%s %s (opensearch=%q watch=%q)", service, version, s.opensearch, os.Getenv("OUTBOX_WATCH_DIR"))
	log.Fatal(httpx.ListenAndServe(addr, auth.Middleware(mux)))
}

func (s *server) ingest(topic string, env envelope.Envelope) {
	d := s.ix.IndexEnvelope(topic, env)
	if s.opensearch != "" {
		if err := s.bulkIndex(d); err != nil {
			log.Printf("opensearch bulk: %v", err)
		}
	}
}

// bulkIndex indexes one doc to OpenSearch via the real bulk API.
func (s *server) bulkIndex(d index.Doc) error {
	meta, _ := json.Marshal(map[string]any{"index": map[string]any{"_index": "nrs-events", "_id": d.ID}})
	var src map[string]any
	if err := json.Unmarshal(d.Raw, &src); err != nil {
		return err
	}
	src["topic"] = d.Topic
	srcBody, _ := json.Marshal(src)
	body := append(append(meta, '\n'), append(srcBody, '\n')...)
	req, err := http.NewRequest(http.MethodPost, s.opensearch+"/_bulk", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &osError{status: resp.StatusCode}
	}
	return nil
}

type osError struct{ status int }

func (e *osError) Error() string { return "opensearch bulk status " + http.StatusText(e.status) }

// watchOutboxDirs polls */outbox.jsonl under dir and indexes new records.
func (s *server) watchOutboxDirs(root string) {
	offsets := map[string]int{}
	for {
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, "outbox.jsonl") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			lines := strings.Split(string(b), "\n")
			seen := offsets[path]
			for i, line := range lines {
				if i < seen || strings.TrimSpace(line) == "" {
					continue
				}
				var rec struct {
					Topic    string            `json:"topic"`
					Envelope envelope.Envelope `json:"envelope"`
				}
				if json.Unmarshal([]byte(line), &rec) == nil && rec.Topic != "" {
					s.ingest(rec.Topic, rec.Envelope)
				}
				offsets[path] = i + 1
			}
			return nil
		})
		time.Sleep(2 * time.Second)
	}
}

func (s *server) indexDoc(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Topic    string          `json:"topic"`
		Envelope json.RawMessage `json:"envelope"`
	}
	if err := httpx.Decode(r, &req); err != nil || len(req.Envelope) == 0 {
		httpx.BadRequest(w, "topic and envelope are required")
		return
	}
	var env envelope.Envelope
	if err := json.Unmarshal(req.Envelope, &env); err != nil {
		httpx.BadRequest(w, "bad envelope: %v", err)
		return
	}
	if env.ID == "" {
		env.ID = envelope.NewULID()
	}
	topic := req.Topic
	if topic == "" {
		topic = env.Type
	}
	s.ingest(topic, env)
	httpx.JSON(w, http.StatusAccepted, map[string]any{"indexed": env.ID, "topic": topic})
}

func (s *server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		httpx.BadRequest(w, "q is required")
		return
	}
	hits := s.ix.Search(q, 50)
	if hits == nil {
		hits = []index.Hit{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"query": q, "count": len(hits), "hits": hits})
}
