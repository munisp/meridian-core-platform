package rulepackschema

// Ceremony-canonical YAML emitter (R4 contract bridge).
//
// Signing contract "meridian-ceremony-canonical-yaml/v1": the signed message
// is the pack mapping WITHOUT the `signed` block, serialised exactly as
// PyYAML does in meridian-rule-packs tools/rpcommon.canonical_bytes:
//
//	yaml.safe_dump(body, sort_keys=True, allow_unicode=True,
//	               default_flow_style=False, width=10**6).encode("utf-8")
//
// The governance ceremony (tools/ceremony.py stage_sign) signs THESE bytes
// with ed25519 (key_id governance-board-2026); they are the source of truth.
// This file re-implements that byte form in Go so the runtime can verify
// ceremony packs without re-signing. Byte-exactness is proven, not assumed:
// the package tests verify REAL ceremony signatures over Go-emitted bytes —
// ed25519 verification succeeds only if the bytes are identical.
//
// PyYAML block-style emission rules reproduced here:
//   - mapping keys sorted (Python str ordering == UTF-8 byte ordering)
//   - 2-space mapping indentation; sequences are indentless under a key
//   - "- " sequence items; nested mappings continue on the "- " line
//   - plain scalars where PyYAML's analysis allows them, otherwise
//     single-quoted ('' escape), otherwise double-quoted with escapes
//   - strings that would implicitly resolve to bool/int/float/null/
//     timestamp/merge/value under the PyYAML (YAML 1.1) resolver are quoted
//   - non-str scalars (int/float/bool/null/timestamp literals) are emitted
//     verbatim from the artifact: ceremony artifacts are themselves written
//     by PyYAML, so the literal text is already the canonical form
//
// Anchors/aliases round-trip like PyYAML (idNNN, document order).
// Fail-closed: multi-document streams, non-mapping roots, non-string keys,
// or tags outside the pack data model are errors, never silently coerced.

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// CanonicalSigningBytesFromYAML returns the exact bytes the ceremony signs
// for the pack carried in artifact (the stored canonical YAML artifact —
// e.g. the vendored pack file or the registry-held YAML). It parses the
// artifact, drops the top-level `signed` block, and re-emits the body in
// the ceremony's canonical PyYAML form.
func CanonicalSigningBytesFromYAML(artifact []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(artifact, &doc); err != nil {
		return nil, fmt.Errorf("rulepack-schema: artifact is not valid YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, errors.New("rulepack-schema: artifact must be a single YAML document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("rulepack-schema: pack document must be a mapping")
	}
	e := &canonEmitter{anchors: map[*yaml.Node]string{}, aliased: map[*yaml.Node]bool{}}
	collectAliasTargets(root, e.aliased)
	pairs, err := e.sortedPairs(root, true)
	if err != nil {
		return nil, err
	}
	if err := e.emitMapping(pairs, 0); err != nil {
		return nil, err
	}
	return []byte(e.sb.String()), nil
}

// canonEmitter carries anchor/alias state. PyYAML re-anchors shared
// (aliased) objects as &idNNN in order of first serialisation and emits
// later references as *idNNN; safe_load/safe_dump round-trips them, so the
// canonical form can contain anchors (real pack rp-wht-2024 does).
type canonEmitter struct {
	sb       strings.Builder
	anchors  map[*yaml.Node]string // aliased node -> assigned anchor id
	aliased  map[*yaml.Node]bool   // nodes that are targets of aliases
	nextID   int
}

// collectAliasTargets marks every node referenced by an alias.
func collectAliasTargets(n *yaml.Node, out map[*yaml.Node]bool) {
	if n == nil {
		return
	}
	if n.Kind == yaml.AliasNode {
		out[n.Alias] = true
		return
	}
	for _, c := range n.Content {
		collectAliasTargets(c, out)
	}
}

// anchorPrefix returns "&idNNN " for the first serialisation of an aliased
// node, assigning ids sequentially in document order (PyYAML behaviour).
func (e *canonEmitter) anchorPrefix(n *yaml.Node) string {
	if !e.aliased[n] {
		return ""
	}
	id, ok := e.anchors[n]
	if !ok {
		e.nextID++
		id = fmt.Sprintf("id%03d", e.nextID)
		e.anchors[n] = id
	}
	return "&" + id + " "
}

// sortedPairs returns the mapping's key/value node pairs sorted by key,
// optionally dropping the top-level `signed` entry. Keys must be strings.
func (e *canonEmitter) sortedPairs(m *yaml.Node, dropSigned bool) ([][2]*yaml.Node, error) {
	pairs := make([][2]*yaml.Node, 0, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
			return nil, errors.New("rulepack-schema: canonical pack keys must be plain strings")
		}
		if dropSigned && k.Value == "signed" {
			continue
		}
		pairs = append(pairs, [2]*yaml.Node{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0].Value < pairs[j][0].Value })
	return pairs, nil
}

func (e *canonEmitter) indent(n int) {
	for i := 0; i < n; i++ {
		e.sb.WriteByte(' ')
	}
}

// emitMapping writes a block mapping at the given indent, PyYAML-style.
func (e *canonEmitter) emitMapping(pairs [][2]*yaml.Node, ind int) error {
	for _, kv := range pairs {
		k, v := kv[0], kv[1]
		key, err := emitStringScalar(k.Value)
		if err != nil {
			return err
		}
		e.indent(ind)
		e.sb.WriteString(key)
		e.sb.WriteByte(':')
		if err := e.emitValue(v, ind); err != nil {
			return err
		}
	}
	return nil
}

// emitValue writes the value part after "key:" (the leading colon is already
// written). ind is the indent of the owning mapping.
func (e *canonEmitter) emitValue(v *yaml.Node, ind int) error {
	prefix := e.anchorPrefix(v)
	switch v.Kind {
	case yaml.ScalarNode:
		s, err := emitScalar(v)
		if err != nil {
			return err
		}
		e.sb.WriteByte(' ')
		e.sb.WriteString(prefix)
		e.sb.WriteString(s)
		e.sb.WriteByte('\n')
		return nil
	case yaml.MappingNode:
		pairs, err := e.sortedPairs(v, false)
		if err != nil {
			return err
		}
		if len(pairs) == 0 {
			e.sb.WriteString(" " + prefix + "{}\n")
			return nil
		}
		if prefix != "" {
			e.sb.WriteByte(' ')
			e.sb.WriteString(prefix[:len(prefix)-1])
		}
		e.sb.WriteByte('\n')
		return e.emitMapping(pairs, ind+2)
	case yaml.SequenceNode:
		if len(v.Content) == 0 {
			e.sb.WriteString(" " + prefix + "[]\n")
			return nil
		}
		if prefix != "" {
			e.sb.WriteByte(' ')
			e.sb.WriteString(prefix[:len(prefix)-1])
		}
		e.sb.WriteByte('\n')
		return e.emitSequence(v.Content, ind) // indentless sequences (PyYAML default)
	case yaml.AliasNode:
		id, ok := e.anchors[v.Alias]
		if !ok {
			return errors.New("rulepack-schema: alias before its anchor in the canonical form")
		}
		e.sb.WriteString(" *" + id + "\n")
		return nil
	default:
		return fmt.Errorf("rulepack-schema: unsupported YAML node kind %d", v.Kind)
	}
}

// emitSequence writes block sequence items at indent ind ("- " style).
func (e *canonEmitter) emitSequence(items []*yaml.Node, ind int) error {
	for _, it := range items {
		e.indent(ind)
		e.sb.WriteByte('-')
		prefix := e.anchorPrefix(it)
		switch it.Kind {
		case yaml.ScalarNode:
			s, err := emitScalar(it)
			if err != nil {
				return err
			}
			e.sb.WriteByte(' ')
			e.sb.WriteString(prefix)
			e.sb.WriteString(s)
			e.sb.WriteByte('\n')
		case yaml.MappingNode:
			pairs, err := e.sortedPairs(it, false)
			if err != nil {
				return err
			}
			if len(pairs) == 0 {
				e.sb.WriteString(" " + prefix + "{}\n")
				continue
			}
			if prefix != "" {
				e.sb.WriteByte(' ')
				e.sb.WriteString(prefix[:len(prefix)-1])
			}
			// First key on the "- " line, rest at ind+2 (PyYAML block style).
			k0, err := emitStringScalar(pairs[0][0].Value)
			if err != nil {
				return err
			}
			e.sb.WriteByte(' ')
			e.sb.WriteString(k0)
			e.sb.WriteByte(':')
			if err := e.emitValue(pairs[0][1], ind+2); err != nil {
				return err
			}
			if err := e.emitMapping(pairs[1:], ind+2); err != nil {
				return err
			}
		case yaml.SequenceNode:
			if len(it.Content) == 0 {
				e.sb.WriteString(" " + prefix + "[]\n")
				continue
			}
			if prefix != "" {
				e.sb.WriteByte(' ')
				e.sb.WriteString(prefix[:len(prefix)-1])
			}
			e.sb.WriteByte(' ')
			if err := e.emitSequenceInline(it.Content, ind+2); err != nil {
				return err
			}
		case yaml.AliasNode:
			id, ok := e.anchors[it.Alias]
			if !ok {
				return errors.New("rulepack-schema: alias before its anchor in the canonical form")
			}
			e.sb.WriteString(" *" + id + "\n")
		default:
			return fmt.Errorf("rulepack-schema: unsupported sequence item kind %d", it.Kind)
		}
	}
	return nil
}

// emitSequenceInline handles the rare "- - nested" sequence-in-sequence case.
func (e *canonEmitter) emitSequenceInline(items []*yaml.Node, ind int) error {
	for i, it := range items {
		if i > 0 {
			e.indent(ind)
		}
		e.sb.WriteByte('-')
		if it.Kind == yaml.ScalarNode {
			s, err := emitScalar(it)
			if err != nil {
				return err
			}
			e.sb.WriteByte(' ')
			e.sb.WriteString(s)
			e.sb.WriteByte('\n')
			continue
		}
		return errors.New("rulepack-schema: non-scalar nested sequence items are not supported in the canonical form")
	}
	return nil
}

// emitScalar renders a scalar node. Non-string scalars keep their artifact
// literal (already canonical — ceremony artifacts are PyYAML-written).
func emitScalar(n *yaml.Node) (string, error) {
	switch n.Tag {
	case "!!str":
		return emitStringScalar(n.Value)
	case "!!int", "!!timestamp":
		return n.Value, nil
	case "!!float":
		return n.Value, nil
	case "!!bool":
		switch n.Value {
		case "true", "false":
			return n.Value, nil
		case "True", "TRUE":
			return "true", nil
		case "False", "FALSE":
			return "false", nil
		}
		return "", fmt.Errorf("rulepack-schema: non-canonical bool literal %q", n.Value)
	case "!!null":
		return "null", nil
	}
	return "", fmt.Errorf("rulepack-schema: unsupported scalar tag %q in canonical pack form", n.Tag)
}

// ---------------------------------------------------------------------------
// PyYAML scalar style analysis (emitter.py analyze_scalar, block context)
// ---------------------------------------------------------------------------

// PyYAML YAML-1.1 implicit resolvers: a string matching any of these is NOT
// emitted plain — PyYAML would read it back as a non-string, so it quotes.
var (
	resBool = regexp.MustCompile(`^(?:yes|Yes|YES|no|No|NO|true|True|TRUE|false|False|FALSE|on|On|ON|off|Off|OFF)$`)
	resFloat = regexp.MustCompile(`^(?:[-+]?(?:[0-9][0-9_]*)\.[0-9_]*(?:[eE][-+][0-9]+)?` +
		`|\.[0-9][0-9_]*(?:[eE][-+][0-9]+)?` +
		`|[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+\.[0-9_]*` +
		`|[-+]?\.(?:inf|Inf|INF)` +
		`|\.(?:nan|NaN|NAN))$`)
	resInt = regexp.MustCompile(`^(?:[-+]?0b[0-1_]+` +
		`|[-+]?0[0-7_]+` +
		`|[-+]?(?:0|[1-9][0-9_]*)` +
		`|[-+]?0x[0-9a-fA-F_]+` +
		`|[-+]?[1-9][0-9_]*(?::[0-5]?[0-9])+)$`)
	resNull      = regexp.MustCompile(`^(?:~|null|Null|NULL|)$`)
	resTimestamp = regexp.MustCompile(`^(?:[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]` +
		`|[0-9][0-9][0-9][0-9]-[0-9][0-9]?-[0-9][0-9]?` +
		`(?:[Tt]|[ \t]+)[0-9][0-9]?:[0-9][0-9]:[0-9][0-9]` +
		`(?:\.[0-9]*)?(?:[ \t]*(?:Z|[-+][0-9][0-9]?(?::[0-9][0-9])?))?)$`)
	resMerge = regexp.MustCompile(`^<<$`)
	resValue = regexp.MustCompile(`^=$`)
)

// resolvesToNonString reports whether PyYAML's implicit resolver would read
// this plain scalar back as something other than a string.
func resolvesToNonString(s string) bool {
	return resBool.MatchString(s) || resFloat.MatchString(s) ||
		resInt.MatchString(s) || resNull.MatchString(s) ||
		resTimestamp.MatchString(s) || resMerge.MatchString(s) ||
		resValue.MatchString(s)
}

// isPrintable mirrors PyYAML's printable check with allow_unicode=True:
// unicode is allowed raw; only C0/C1 controls (except none here — newlines
// are handled by the double-quote path), the BOM and surrogates force
// escaping.
func isPrintable(r rune) bool {
	if r == '\n' || r == '\t' {
		return true // representable, but forces non-plain style
	}
	if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
		return false
	}
	if r == 0xfeff || (r >= 0xd800 && r <= 0xdfff) {
		return false
	}
	return unicode.IsPrint(r) || r >= 0xa0
}

// allowPlain mirrors PyYAML emitter.analyze_scalar for block context.
func allowPlain(s string) bool {
	if s == "" {
		return false
	}
	// leading/trailing spaces or newline-adjacent spaces
	if s[0] == ' ' || s[len(s)-1] == ' ' {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	// indicator characters that cannot START a plain scalar
	if strings.ContainsRune(",[]{}#&*!|>'\"%@`", r) {
		return false
	}
	if r == '-' || r == '?' || r == ':' {
		if len(s) == 1 || s[1] == ' ' {
			return false
		}
	}
	// ": " anywhere, trailing ':', " #" anywhere
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ':':
			if i+1 == len(s) || s[i+1] == ' ' {
				return false
			}
		case '#':
			if i > 0 && s[i-1] == ' ' {
				return false
			}
		}
	}
	// non-printable / newline / tab force a quoted style
	for _, c := range s {
		if c == '\n' || c == '\t' || !isPrintable(c) {
			return false
		}
	}
	return true
}

// emitStringScalar renders a Go string as a PyYAML block-context scalar:
// plain when allowed and unambiguous, else single-quoted, else
// double-quoted with escapes.
func emitStringScalar(s string) (string, error) {
	if allowPlain(s) && !resolvesToNonString(s) {
		return s, nil
	}
	// single-quoted form: any printable text without \n (PyYAML folds
	// newlines in quoted scalars; packs never contain them — fail closed).
	singleOK := true
	for _, c := range s {
		if c == '\n' || c == '\t' || !isPrintable(c) {
			singleOK = false
			break
		}
	}
	if singleOK {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
	}
	// double-quoted fallback with YAML escapes
	var sb strings.Builder
	sb.WriteByte('"')
	for _, c := range s {
		switch c {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if !isPrintable(c) {
				if c <= 0xff {
					fmt.Fprintf(&sb, `\x%02X`, c)
				} else if c <= 0xffff {
					fmt.Fprintf(&sb, `\u%04X`, c)
				} else {
					fmt.Fprintf(&sb, `\U%08X`, c)
				}
			} else {
				sb.WriteRune(c)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String(), nil
}
