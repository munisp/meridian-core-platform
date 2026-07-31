// search-indexer — outbox->index service (SPEC 2). Ingests event envelopes
// (bus subscription to INDEX_TOPICS, POST /v1/index, OUTBOX_WATCH_DIR poll),
// indexes to OpenSearch when OPENSEARCH_URL is set (real bulk API) and to
// the embedded local index always (dev fallback searched by /v1/search).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v2"
	"github.com/opensearch-project/opensearch-go/v2/opensearchapi"

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
	ix *index.Index
	os *opensearch.Client // nil in dev profile
}

// osIndexFor maps a topic family to the OpenSearch index name
// (HARDENING H3: nrs-{family}-v1).
func osIndexFor(topic string) string {
	family := "events"
	parts := strings.Split(topic, ".")
	if len(parts) >= 2 && parts[1] != "" {
		family = parts[1]
	}
	return "nrs-" + family + "-v1"
}

func main() {
	dir := httpx.Env("DATA_DIR", "./data")
	st, err := store.OpenFromEnv(dir)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{ix: index.New(st)}
	// HARDENING H1: OPENSEARCH_URL set -> real bulk client (profile=prod).
	if url := strings.TrimRight(os.Getenv("OPENSEARCH_URL"), "/"); url != "" {
		cfg := opensearch.Config{
			Addresses: []string{url},
			Username:  os.Getenv("OPENSEARCH_USERNAME"),
			Password:  os.Getenv("OPENSEARCH_PASSWORD"),
		}
		osc, err := opensearch.NewClient(cfg)
		if err != nil {
			log.Printf("profile=dev component=search-indexer opensearch client init failed (%v); local index only", err)
		} else {
			s.os = osc
			log.Printf("profile=prod component=search-indexer opensearch=%s", url)
		}
	} else {
		log.Printf("profile=dev component=search-indexer local json index")
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
			"docs": s.ix.Count(), "opensearch": s.os != nil,
		})
	})

	addr := ":" + httpx.Port("8008")
	log.Printf("%s %s (opensearch=%v watch=%q)", service, version, s.os != nil, os.Getenv("OUTBOX_WATCH_DIR"))
	log.Fatal(httpx.ListenAndServe(addr, auth.Middleware(mux)))
}

func (s *server) ingest(topic string, env envelope.Envelope) {
	d := s.ix.IndexEnvelope(topic, env)
	if s.os != nil {
		if err := s.bulkIndex(d); err != nil {
			log.Printf("opensearch bulk: %v", err)
		}
	}
}

// bulkIndex indexes one doc to OpenSearch via the real bulk API
// (opensearch-go v2), index nrs-{family}-v1.
func (s *server) bulkIndex(d index.Doc) error {
	meta, _ := json.Marshal(map[string]any{"index": map[string]any{"_index": osIndexFor(d.Topic), "_id": d.ID}})
	var src map[string]any
	if err := json.Unmarshal(d.Raw, &src); err != nil {
		return err
	}
	src["topic"] = d.Topic
	srcBody, _ := json.Marshal(src)
	body := bytes.NewReader(append(append(meta, '\n'), append(srcBody, '\n')...))
	req := opensearchapi.BulkRequest{Body: body}
	resp, err := req.Do(context.Background(), s.os)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.IsError() {
		return fmt.Errorf("opensearch bulk status %s", resp.Status())
	}
	return nil
}

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
