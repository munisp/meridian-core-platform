// Package permifymodels is the dev file-backed relation checker (SPEC 2).
// The canonical Permify DSL lives in schemas/*.perm for the Permify server;
// this evaluator implements the subset used in dev: direct tuples, union of
// relations, and dotted references (doc.matter.counsel), BFS-resolved.
package permifymodels

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Tuple is one object#relation@subject fact.
type Tuple struct {
	Object   string `json:"object"`   // e.g. doc:d1
	Relation string `json:"relation"` // e.g. matter
	Subject  string `json:"subject"`  // e.g. user:u1 or matter:m1
}

// RelationDef declares a permission/relation as a union of references:
// each entry is either a local relation name ("counsel") or a dotted
// walk ("matter.counsel": through the matter tuple then its counsel).
type RelationDef struct {
	Entity string   `json:"entity"`
	Name   string   `json:"name"`
	Union  []string `json:"union"`
}

// Checker evaluates relation checks over tuples + defs.
type Checker struct {
	tuples []Tuple
	defs   map[string]RelationDef // key entity:name
}

// NewChecker builds a checker from tuples and relation definitions.
func NewChecker(tuples []Tuple, defs []RelationDef) *Checker {
	c := &Checker{tuples: tuples, defs: map[string]RelationDef{}}
	for _, d := range defs {
		c.defs[d.Entity+":"+d.Name] = d
	}
	return c
}

// LoadTuplesFile loads tuples from a JSON file (tuples.example.json shape).
func LoadTuplesFile(path string) ([]Tuple, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Tuples []Tuple `json:"tuples"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("tuples file: %w", err)
	}
	return f.Tuples, nil
}

func entityOf(object string) string {
	if i := strings.Index(object, ":"); i > 0 {
		return object[:i]
	}
	return object
}

// Check reports whether subject holds object#permission (or relation).
func (c *Checker) Check(object, relation, subject string) bool {
	visited := map[string]bool{}
	return c.expand(object, relation, subject, visited, 0)
}

func (c *Checker) expand(object, relation, subject string, visited map[string]bool, depth int) bool {
	if depth > 16 {
		return false
	}
	key := object + "#" + relation + "@" + subject
	if visited[key] {
		return false
	}
	visited[key] = true
	// 1. direct tuple
	for _, t := range c.tuples {
		if t.Object == object && t.Relation == relation && t.Subject == subject {
			return true
		}
	}
	// 2. definition union
	def, ok := c.defs[entityOf(object)+":"+relation]
	if !ok {
		return false
	}
	for _, ref := range def.Union {
		parts := strings.SplitN(ref, ".", 2)
		if len(parts) == 1 {
			// local relation/permission on the same object
			if c.expand(object, parts[0], subject, visited, depth+1) {
				return true
			}
			// also treat union entry as direct tuple relation
			for _, t := range c.tuples {
				if t.Object == object && t.Relation == parts[0] && t.Subject == subject {
					return true
				}
			}
			continue
		}
		// dotted walk: object --parts[0]--> intermediate --parts[1]--> subject
		for _, t := range c.tuples {
			if t.Object == object && t.Relation == parts[0] {
				if c.expand(t.Subject, parts[1], subject, visited, depth+1) {
					return true
				}
			}
		}
	}
	return false
}
