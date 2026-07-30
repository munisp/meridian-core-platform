// Package index is the dev local search index: an inverted index over
// envelope documents with AND query semantics and term-frequency ranking.
// When OPENSEARCH_URL is set the service also bulk-indexes to OpenSearch
// (real HTTP); this local index remains the dev fallback (SPEC 2).
package index

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
	"github.com/munisp/meridian-core-platform/packages/events/store"
)

// Doc is one indexed envelope.
type Doc struct {
	ID     string          `json:"id"`
	Topic  string          `json:"topic"`
	Type   string          `json:"type"`
	Source string          `json:"source"`
	Time   string          `json:"time"`
	Text   string          `json:"text"` // flattened searchable text
	Raw    json.RawMessage `json:"raw"`
}

// Hit is a search result.
type Hit struct {
	Doc   Doc     `json:"doc"`
	Score float64 `json:"score"`
}

// Index is the inverted index.
type Index struct {
	mu       sync.RWMutex
	docs     map[string]*Doc
	postings map[string]map[string]int // term -> docID -> term frequency
	st       store.DocStore              // persistence (docs collection)
}

// New creates/restores an index backed by the given store (may be nil).
func New(st store.DocStore) *Index {
	ix := &Index{docs: map[string]*Doc{}, postings: map[string]map[string]int{}, st: st}
	if st != nil {
		raws, err := st.List("docs")
		if err == nil {
			for _, raw := range raws {
				var d Doc
				if json.Unmarshal(raw, &d) == nil {
					ix.addDoc(d, false)
				}
			}
		}
	}
	return ix
}

// Tokenize splits text into lowercase alphanumeric terms.
func Tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// flatten extracts searchable text from arbitrary JSON.
func flatten(sb *strings.Builder, v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			sb.WriteString(k)
			sb.WriteByte(' ')
			flatten(sb, vv)
		}
	case []any:
		for _, vv := range t {
			flatten(sb, vv)
		}
	case string:
		sb.WriteString(t)
		sb.WriteByte(' ')
	case float64, bool:
		b, _ := json.Marshal(t)
		sb.Write(b)
		sb.WriteByte(' ')
	}
}

// IndexEnvelope adds one event envelope under a topic.
func (ix *Index) IndexEnvelope(topic string, env envelope.Envelope) Doc {
	var sb strings.Builder
	sb.WriteString(env.Type + " " + env.Source + " " + env.TenantID + " ")
	var data any
	if json.Unmarshal(env.Data, &data) == nil {
		flatten(&sb, data)
	}
	raw, _ := json.Marshal(env)
	d := Doc{
		ID:     env.ID,
		Topic:  topic,
		Type:   env.Type,
		Source: env.Source,
		Time:   env.Time,
		Text:   sb.String(),
		Raw:    raw,
	}
	ix.addDoc(d, true)
	return d
}

func (ix *Index) addDoc(d Doc, persist bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if _, exists := ix.docs[d.ID]; exists {
		return // idempotent
	}
	cp := d
	ix.docs[d.ID] = &cp
	freq := map[string]int{}
	for _, tok := range Tokenize(d.Text) {
		freq[tok]++
	}
	for term, n := range freq {
		m := ix.postings[term]
		if m == nil {
			m = map[string]int{}
			ix.postings[term] = m
		}
		m[d.ID] = n
	}
	if persist && ix.st != nil {
		_ = ix.st.Put("docs", d.ID, d)
	}
}

// Search runs an AND query with tf ranking.
func (ix *Index) Search(q string, limit int) []Hit {
	terms := Tokenize(q)
	if len(terms) == 0 {
		return nil
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var candidate map[string]float64
	for _, term := range terms {
		posts := ix.postings[term]
		next := map[string]float64{}
		if candidate == nil {
			for id, tf := range posts {
				next[id] = float64(tf)
			}
		} else {
			for id, score := range candidate {
				if tf, ok := posts[id]; ok {
					next[id] = score + float64(tf)
				}
			}
		}
		candidate = next
	}
	hits := make([]Hit, 0, len(candidate))
	for id, score := range candidate {
		hits = append(hits, Hit{Doc: *ix.docs[id], Score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Doc.Time > hits[j].Doc.Time
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// Count returns the number of indexed docs.
func (ix *Index) Count() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.docs)
}
