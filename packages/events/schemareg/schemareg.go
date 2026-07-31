// Package schemareg is the canonical schema registry for all nrs.* topics
// (audit I3/I8). Every published topic has a JSON Schema (draft 2020-12)
// for its envelope `data` payload; the envelope shape itself is validated
// against schemas/envelope.schema.json.
//
// The registry ships an embedded dev store (schemas/*.json + topics.json,
// mirroring packages/schemas/jsonschema plus schemas for previously
// schema-less published topics) so dev/test needs no external service.
// Producers should register/validate via ValidateBeforePublish in the bus
// package (dev: warn; prod: reject unregistered/invalid).
//
// Compatibility: CheckCompatibility enforces BACKWARD compatibility — a
// new schema version must accept everything the previous version accepted
// (no new required fields, no narrowed types/enums on existing fields).
package schemareg

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
)

//go:embed schemas/*.json topics.json
var devFS embed.FS

// CatalogEntry describes one catalogued topic (audit I8).
type CatalogEntry struct {
	Topic         string `json:"topic"`
	Owner         string `json:"owner"`
	Schema        string `json:"schema"`
	PII           string `json:"pii"`
	RetentionDays int    `json:"retention_days"`
}

type catalogDoc struct {
	Version int            `json:"version"`
	Topics  []CatalogEntry `json:"topics"`
}

// Schema is a parsed JSON Schema (subset used by Meridian schemas).
type Schema = map[string]any

// Registry maps topic -> JSON Schema for the envelope data payload.
type Registry struct {
	mu      sync.RWMutex
	schemas map[string]Schema
	catalog []CatalogEntry
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{schemas: map[string]Schema{}}
}

// NewDev returns a registry pre-loaded with the embedded dev store:
// every catalogued topic registered to its schema (permissive
// nrs.generic.v1 where a dedicated schema has not landed yet).
func NewDev() (*Registry, error) {
	r := New()
	var cat catalogDoc
	b, err := devFS.ReadFile("topics.json")
	if err != nil {
		return nil, fmt.Errorf("schemareg: read topics.json: %w", err)
	}
	if err := json.Unmarshal(b, &cat); err != nil {
		return nil, fmt.Errorf("schemareg: parse topics.json: %w", err)
	}
	r.catalog = cat.Topics
	for _, e := range cat.Topics {
		name := e.Schema
		if name == "generic" {
			name = "nrs.generic.v1"
		}
		raw, err := devFS.ReadFile("schemas/" + name + ".schema.json")
		if err != nil {
			return nil, fmt.Errorf("schemareg: schema for %s: %w", e.Topic, err)
		}
		if err := r.Register(e.Topic, raw); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Catalog returns the topic catalog (empty for a hand-built registry).
func (r *Registry) Catalog() []CatalogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]CatalogEntry(nil), r.catalog...)
}

// Register adds (or replaces) the data schema for a topic.
func (r *Registry) Register(topic string, schema json.RawMessage) error {
	if !ValidTopic(topic) {
		return fmt.Errorf("schemareg: invalid topic name %q", topic)
	}
	var s Schema
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("schemareg: parse schema for %s: %w", topic, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.schemas[topic] = s
	return nil
}

// Lookup returns the schema for a topic.
func (r *Registry) Lookup(topic string) (Schema, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.schemas[topic]
	return s, ok
}

// Topics returns the sorted list of registered topics.
func (r *Registry) Topics() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.schemas))
	for t := range r.schemas {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

var topicRE = regexp.MustCompile(`^nrs\.[a-z0-9._]+\.v\d+$`)

// ValidTopic reports whether a topic name follows the nrs.*.vN convention.
func ValidTopic(topic string) bool { return topicRE.MatchString(topic) }

// ErrUnregistered is returned (or warned) when a topic has no schema.
type ErrUnregistered struct{ Topic string }

func (e *ErrUnregistered) Error() string {
	return fmt.Sprintf("schemareg: topic %s is not registered", e.Topic)
}

// ErrInvalid wraps one or more schema validation failures.
type ErrInvalid struct {
	Topic  string
	Errors []string
}

func (e *ErrInvalid) Error() string {
	return fmt.Sprintf("schemareg: %s payload invalid: %s", e.Topic, strings.Join(e.Errors, "; "))
}

// ValidateData validates an envelope data payload against the topic schema.
func (r *Registry) ValidateData(topic string, data json.RawMessage) error {
	s, ok := r.Lookup(topic)
	if !ok {
		return &ErrUnregistered{Topic: topic}
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return &ErrInvalid{Topic: topic, Errors: []string{"data is not valid JSON: " + err.Error()}}
	}
	if errs := Validate(s, v, "$"); len(errs) > 0 {
		return &ErrInvalid{Topic: topic, Errors: errs}
	}
	return nil
}

// ValidateEnvelope validates the envelope shape and its data payload.
func (r *Registry) ValidateEnvelope(env envelope.Envelope) error {
	var problems []string
	if len(env.ID) != 26 {
		problems = append(problems, "id must be a 26-char ULID")
	}
	if !ValidTopic(env.Type) {
		problems = append(problems, fmt.Sprintf("type %q does not match nrs.*.vN", env.Type))
	}
	if env.Source == "" {
		problems = append(problems, "source is required")
	}
	if env.Time == "" {
		problems = append(problems, "time is required")
	}
	if len(env.TraceID) != 32 {
		problems = append(problems, "trace_id must be 32 hex chars")
	}
	if len(problems) > 0 {
		return &ErrInvalid{Topic: env.Type, Errors: problems}
	}
	return r.ValidateData(env.Type, env.Data)
}

// CheckCompatibility enforces BACKWARD compatibility between the currently
// registered schema for topic and candidate: candidate must not require
// fields the old schema did not require, must not change the type of a
// common property, and must not narrow an existing enum. Unregistered
// topics are trivially compatible.
func (r *Registry) CheckCompatibility(topic string, candidate json.RawMessage) error {
	old, ok := r.Lookup(topic)
	if !ok {
		return nil
	}
	var newS Schema
	if err := json.Unmarshal(candidate, &newS); err != nil {
		return fmt.Errorf("schemareg: parse candidate schema: %w", err)
	}
	if problems := compatProblems("", old, newS); len(problems) > 0 {
		return &ErrInvalid{Topic: topic, Errors: problems}
	}
	return nil
}

func compatProblems(path string, old, newS Schema) []string {
	var out []string
	oldReq := stringSet(old["required"])
	for _, req := range toStrings(newS["required"]) {
		if !oldReq[req] {
			out = append(out, fmt.Sprintf("%s: new required field %q breaks backward compatibility", dotpath(path), req))
		}
	}
	oldProps, _ := old["properties"].(map[string]any)
	newProps, _ := newS["properties"].(map[string]any)
	for name, op := range oldProps {
		np, ok := newProps[name]
		if !ok {
			continue // dropping a field is backward compatible for readers of old data
		}
		om, _ := op.(map[string]any)
		nm, _ := np.(map[string]any)
		if ot, nt := fmt.Sprint(om["type"]), fmt.Sprint(nm["type"]); om["type"] != nil && nm["type"] != nil && ot != nt {
			out = append(out, fmt.Sprintf("%s: type of %q changed %s -> %s", dotpath(path), name, ot, nt))
		}
		if oe, ne := toStrings(om["enum"]), toStrings(nm["enum"]); len(oe) > 0 && len(ne) > 0 {
			allowed := stringSet(nm["enum"])
			for _, v := range oe {
				if !allowed[v] {
					out = append(out, fmt.Sprintf("%s: enum of %q narrowed (removed %q)", dotpath(path), name, v))
				}
			}
		}
		if om["properties"] != nil || nm["properties"] != nil {
			out = append(out, compatProblems(dotpath(path)+"."+name, om, nm)...)
		}
	}
	return out
}

func dotpath(p string) string {
	if p == "" {
		return "schema"
	}
	return "schema" + p
}

func stringSet(v any) map[string]bool {
	out := map[string]bool{}
	for _, s := range toStrings(v) {
		out[s] = true
	}
	return out
}

func toStrings(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
