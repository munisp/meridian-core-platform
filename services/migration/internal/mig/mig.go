// Package mig implements legacy data-migration tooling (M1): streaming
// CSV/JSONL ingest of legacy taxpayers, filings and payments, staging
// validation (schema, TIN format/checksum, duplicate detection vs the
// live tin-graph, referential checks), content-addressed batches with
// signed reconciliation manifests and post-import PASS/FAIL proofs.
//
// Money is integer kobo everywhere; fractional or negative amounts are
// rejected. The pipeline is fail-closed: a commit with no signing key is
// an error, a failed write rolls the whole batch back (no partial-file
// corruption), and dry-run mode never touches the store.
//
// Reconciliation manifest (per batch): row counts by entity, sums of
// amounts in kobo, sha256 of the canonical payload, and a per-row hash
// chain (h_i = sha256(h_{i-1} || canonical_row)), HMAC-signed with the
// service key. Batches are content-addressed: re-running the same files
// yields the same batch id and the same manifest, with no duplicates.
package mig

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// EntityKind enumerates the importable legacy record types.
type EntityKind string

const (
	Taxpayers EntityKind = "taxpayers"
	Filings   EntityKind = "filings"
	Payments  EntityKind = "payments"
)

// Mode selects dry-run (validate + proof, no writes) or commit.
type Mode string

const (
	DryRun Mode = "dry_run"
	Commit Mode = "commit"
)

// Row types. Kobo fields are integer kobo; JSON tags are the canonical
// payload field names (marshal of these structs IS the canonical row).
type TaxpayerRow struct {
	TIN              string `json:"tin"`
	Name             string `json:"name"`
	Type             string `json:"type"` // individual|company
	RegistrationDate string `json:"registration_date"`
	Status           string `json:"status"`
}

type FilingRow struct {
	TIN          string `json:"tin"`
	Period       string `json:"period"` // YYYY-MM
	TaxType      string `json:"tax_type"`
	AssessedKobo int64  `json:"assessed_kobo"`
}

type PaymentRow struct {
	TIN        string `json:"tin"`
	Reference  string `json:"reference"`
	AmountKobo int64  `json:"amount_kobo"`
	Date       string `json:"date"` // YYYY-MM-DD
	Channel    string `json:"channel"`
}

// RowError is one row-level error in the batch error ledger.
type RowError struct {
	Entity EntityKind `json:"entity"`
	File   string     `json:"file"`
	Line   int        `json:"line"`
	Err    string     `json:"err"`
}

// SourceFile is one ingest input.
type SourceFile struct {
	Entity EntityKind
	Format string // csv|jsonl
	Name   string
	R      io.Reader
}

// Store is the minimal document-store contract the importer needs.
// The service layer provides a durable implementation.
type Store interface {
	Get(coll, id string) (json.RawMessage, error)
	Put(coll, id string, doc json.RawMessage) error
	Delete(coll, id string) error
	List(coll string) ([]json.RawMessage, error)
}

// ErrNotFound mirrors the platform store sentinel.
var ErrNotFound = errors.New("not found")

// LiveIndex checks duplicates/references against live tin-graph.
type LiveIndex interface {
	LookupTIN(tin string) (exists bool, entityID string, err error)
}

// Deps are the importer dependencies.
type Deps struct {
	Store      Store
	Live       LiveIndex // may be nil (checks skipped, noted in manifest)
	SigningKey []byte    // REQUIRED for commit (fail-closed); dry-run may use nil
	KeyID      string    // recorded on the manifest (keep sim tags honest)
}

// Options control one import run.
type Options struct {
	Mode           Mode
	StrictChecksum bool // enforce the TIN check-digit rule (legacy files often predate it)
}

// --- TIN format + checksum rules ---

var tinRe = regexp.MustCompile(`^\d{8}-\d{4}$`)

// ValidTINFormat reports whether tin matches NNNNNNNN-NNNN.
func ValidTINFormat(tin string) bool { return tinRe.MatchString(tin) }

// TINChecksumOK applies the documented check-digit rule: over the 12
// digits, the 12th is a Luhn-style check digit of the first 11 (double
// every second digit from the right of the payload, sum digits,
// check = (10 - sum mod 10) mod 10).
func TINChecksumOK(tin string) bool {
	if !ValidTINFormat(tin) {
		return false
	}
	digits := make([]int, 0, 12)
	for _, r := range tin {
		if r >= '0' && r <= '9' {
			digits = append(digits, int(r-'0'))
		}
	}
	sum := 0
	for i := 0; i < 11; i++ {
		d := digits[i]
		if (10-i)%2 == 1 { // every second digit from the right of the 11-digit payload
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return (10-sum%10)%10 == digits[11]
}

// TINWithCheckDigit returns a format-valid TIN whose check digit is fixed
// up — used by tests and by operators pre-cleaning legacy extracts.
func TINWithCheckDigit(tin string) (string, error) {
	if !ValidTINFormat(tin) {
		return "", fmt.Errorf("bad TIN format %q", tin)
	}
	digits := make([]int, 0, 12)
	for _, r := range tin {
		if r >= '0' && r <= '9' {
			digits = append(digits, int(r-'0'))
		}
	}
	sum := 0
	for i := 0; i < 11; i++ {
		d := digits[i]
		if (10-i)%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	digits[11] = (10 - sum%10) % 10
	return fmt.Sprintf("%d%d%d%d%d%d%d%d-%d%d%d%d",
		digits[0], digits[1], digits[2], digits[3], digits[4], digits[5], digits[6], digits[7],
		digits[8], digits[9], digits[10], digits[11]), nil
}

// --- manifest + proof ---

// Manifest is the signed per-batch reconciliation proof input.
// BatchID, SHA256, HashChainRoot and the signature are all computed over
// canonical content only — CreatedAt/Committed are excluded, so re-running
// the same files reproduces the SAME manifest (idempotent re-run).
type Manifest struct {
	Version       int              `json:"version"`
	BatchID       string           `json:"batch_id"`
	Mode          Mode             `json:"mode"`
	Counts        map[string]int   `json:"counts"`          // per entity + rejected + duplicates
	SumsKobo      map[string]int64 `json:"sums_kobo"`       // assessed_kobo, payments_kobo
	SHA256        string           `json:"sha256"`          // canonical payload digest
	HashChainRoot string           `json:"hash_chain_root"` // last link of the per-row chain
	RowHashes     []string         `json:"row_hashes"`      // per-row chain links (tamper evidence)
	RowCount      int              `json:"row_count"`
	KeyID         string           `json:"key_id"`
	Signature     string           `json:"signature"` // HMAC-SHA256 over signedPayload()
	Notes         []string         `json:"notes,omitempty"`
	Committed     bool             `json:"committed"`
	CreatedAt     string           `json:"created_at"` // excluded from signature + batch id
}

// signedPayload is the canonical byte range the HMAC covers.
func (m *Manifest) signedPayload() []byte {
	type signed struct {
		Version       int              `json:"version"`
		BatchID       string           `json:"batch_id"`
		Mode          Mode             `json:"mode"`
		Counts        map[string]int   `json:"counts"`
		SumsKobo      map[string]int64 `json:"sums_kobo"`
		SHA256        string           `json:"sha256"`
		HashChainRoot string           `json:"hash_chain_root"`
		RowHashes     []string         `json:"row_hashes"`
		RowCount      int              `json:"row_count"`
		KeyID         string           `json:"key_id"`
		Committed     bool             `json:"committed"`
	}
	b, _ := json.Marshal(signed{m.Version, m.BatchID, m.Mode, m.Counts, m.SumsKobo,
		m.SHA256, m.HashChainRoot, m.RowHashes, m.RowCount, m.KeyID, m.Committed})
	return b
}

// SignManifest HMAC-signs the manifest in place.
func SignManifest(m *Manifest, key []byte) {
	mac := hmac.New(sha256.New, key)
	mac.Write(m.signedPayload())
	m.Signature = hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks the manifest HMAC.
func VerifySignature(m *Manifest, key []byte) bool {
	mac := hmac.New(sha256.New, key)
	mac.Write(m.signedPayload())
	want := mac.Sum(nil)
	got, err := hex.DecodeString(m.Signature)
	if err != nil {
		return false
	}
	return hmac.Equal(want, got)
}

// Check is one reconciliation assertion in a proof document.
type Check struct {
	Name     string `json:"name"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	OK       bool   `json:"ok"`
}

// Proof is the post-import verification verdict (JSON document; Summary
// is the human-readable rendering).
type Proof struct {
	BatchID   string  `json:"batch_id"`
	Verdict   string  `json:"verdict"` // PASS|FAIL
	Checks    []Check `json:"checks"`
	Summary   string  `json:"summary"`
	CreatedAt string  `json:"created_at"`
}

// --- parsing ---

func parseKobo(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("amount required")
	}
	if strings.ContainsAny(s, ".eE") {
		return 0, fmt.Errorf("amount %q is not integer kobo", s)
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("amount %q is not integer kobo", s)
	}
	if v < 0 {
		return 0, fmt.Errorf("amount %d is negative", v)
	}
	return v, nil
}

func validDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func validPeriod(s string) bool {
	_, err := time.Parse("2006-01", s)
	return err == nil
}

// rawRow is the staging representation before entity decode.
type stagedRow struct {
	entity   EntityKind
	line     int
	file     string
	taxpayer *TaxpayerRow
	filing   *FilingRow
	payment  *PaymentRow
}

// canonical returns the canonical JSON encoding of the staged row.
func (s stagedRow) canonical() []byte {
	var b []byte
	switch s.entity {
	case Taxpayers:
		b, _ = json.Marshal(s.taxpayer)
	case Filings:
		b, _ = json.Marshal(s.filing)
	case Payments:
		b, _ = json.Marshal(s.payment)
	}
	// entity tag binds the hash to the row type
	return append([]byte(`{"entity":"`+s.entity+`","row":`), append(b, '}')...)
}

func csvHeaderIndex(header []string) map[string]int {
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return idx
}

func field(rec []string, idx map[string]int, name string) string {
	i, ok := idx[name]
	if !ok || i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}

// parseCSV streams one CSV file, invoking yield per data row. Malformed
// rows go to the error ledger and never abort the stream.
func parseCSV(entity EntityKind, name string, r io.Reader, errs *[]RowError, yield func(line int, rec map[string]string)) error {
	rd := csv.NewReader(r)
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	header, err := rd.Read()
	if err != nil {
		return fmt.Errorf("%s: read header: %w", name, err)
	}
	idx := csvHeaderIndex(header)
	line := 1
	for {
		rec, err := rd.Read()
		line++
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			*errs = append(*errs, RowError{Entity: entity, File: name, Line: line, Err: "csv: " + err.Error()})
			continue
		}
		m := map[string]string{}
		for k := range idx {
			m[k] = field(rec, idx, k)
		}
		yield(line, m)
	}
}

// parseJSONL streams one JSONL file. Kobo fields are decoded via
// json.Number so fractional amounts are rejected, not truncated.
func parseJSONL(entity EntityKind, name string, r io.Reader, errs *[]RowError, yield func(line int, rec map[string]string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			*errs = append(*errs, RowError{Entity: entity, File: name, Line: line, Err: "json: " + err.Error()})
			continue
		}
		out := map[string]string{}
		for k, v := range m {
			switch t := v.(type) {
			case string:
				out[k] = t
			case json.Number:
				out[k] = t.String()
			default:
				out[k] = fmt.Sprintf("%v", t)
			}
		}
		yield(line, out)
	}
	return sc.Err()
}

// --- import pipeline ---

// Result is the outcome of Import.
type Result struct {
	Manifest *Manifest  `json:"manifest"`
	Errors   []RowError `json:"errors"`
}

// Import runs the full pipeline: parse (streaming) -> validate -> stage
// -> (commit only) atomic batch write + manifest persist, or (dry-run)
// proof without any store writes. Idempotent: a batch id that already
// exists returns its stored manifest unchanged.
func Import(deps Deps, files []SourceFile, opts Options, now time.Time) (*Result, error) {
	if opts.Mode != DryRun && opts.Mode != Commit {
		return nil, fmt.Errorf("mode must be dry_run or commit")
	}
	if opts.Mode == Commit && len(deps.SigningKey) == 0 {
		return nil, fmt.Errorf("fail-closed: signing key required for commit mode")
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("at least one source file required")
	}

	var rows []stagedRow
	var errs []RowError
	seen := map[string]bool{} // intra-batch duplicate detection (per entity key)

	addRow := func(entity EntityKind, file string, line int, m map[string]string) {
		sr, err := decodeRow(entity, m)
		if err != nil {
			errs = append(errs, RowError{Entity: entity, File: file, Line: line, Err: err.Error()})
			return
		}
		if err := validateRow(entity, sr, opts.StrictChecksum); err != nil {
			errs = append(errs, RowError{Entity: entity, File: file, Line: line, Err: err.Error()})
			return
		}
		key := dedupKey(sr)
		if seen[key] {
			errs = append(errs, RowError{Entity: entity, File: file, Line: line, Err: "duplicate row within batch"})
			return
		}
		seen[key] = true
		rows = append(rows, sr)
	}

	for _, f := range files {
		switch f.Format {
		case "csv":
			if err := parseCSV(f.Entity, f.Name, f.R, &errs, func(line int, m map[string]string) { addRow(f.Entity, f.Name, line, m) }); err != nil {
				return nil, err
			}
		case "jsonl":
			if err := parseJSONL(f.Entity, f.Name, f.R, &errs, func(line int, m map[string]string) { addRow(f.Entity, f.Name, line, m) }); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%s: unknown format %q (csv|jsonl)", f.Name, f.Format)
		}
	}

	// duplicate detection vs live tin-graph + referential checks
	var liveNotes []string
	valid := rows[:0]
	batchTINs := map[string]bool{}
	for _, r := range rows {
		if r.entity == Taxpayers {
			batchTINs[r.taxpayer.TIN] = true
		}
	}
	for _, r := range rows {
		if r.entity == Taxpayers && deps.Live != nil {
			exists, entityID, err := deps.Live.LookupTIN(r.taxpayer.TIN)
			if err != nil {
				return nil, fmt.Errorf("fail-closed: live tin-graph lookup for %s: %w", r.taxpayer.TIN, err)
			}
			if exists {
				errs = append(errs, RowError{Entity: Taxpayers, File: r.file, Line: r.line,
					Err: "duplicate TIN already live in tin-graph (entity " + entityID + ")"})
				continue
			}
		}
		if r.entity != Taxpayers {
			tin := rowTIN(r)
			if !batchTINs[tin] {
				exists := false
				if deps.Live != nil {
					ex, _, err := deps.Live.LookupTIN(tin)
					if err != nil {
						return nil, fmt.Errorf("fail-closed: live tin-graph lookup for %s: %w", tin, err)
					}
					exists = ex
				}
				if !exists {
					errs = append(errs, RowError{Entity: r.entity, File: r.file, Line: r.line,
						Err: fmt.Sprintf("referential check failed: no taxpayer for TIN %s", tin)})
					continue
				}
			}
		}
		valid = append(valid, r)
	}
	if deps.Live == nil {
		liveNotes = append(liveNotes, "live tin-graph checks skipped (no index configured) [simulated]")
	}

	m := buildManifest(valid, errs, opts, liveNotes, now)
	m.KeyID = deps.KeyID

	if opts.Mode == Commit {
		// content-addressed idempotency: batch already imported?
		if raw, err := deps.Store.Get("mig_batches", m.BatchID); err == nil {
			var existing Manifest
			if json.Unmarshal(raw, &existing) == nil {
				existing.Notes = append(existing.Notes, "idempotent re-run: batch already committed, no duplicates written")
				return &Result{Manifest: &existing, Errors: errs}, nil
			}
		}
		if err := commitBatch(deps.Store, m, valid); err != nil {
			return nil, err
		}
		m.Committed = true
		SignManifest(m, deps.SigningKey)
		if err := putJSON(deps.Store, "mig_batches", m.BatchID, m); err != nil {
			return nil, fmt.Errorf("persist manifest: %w", err)
		}
	} else if len(deps.SigningKey) > 0 {
		SignManifest(m, deps.SigningKey)
	}
	return &Result{Manifest: m, Errors: errs}, nil
}

func rowTIN(r stagedRow) string {
	switch r.entity {
	case Filings:
		return r.filing.TIN
	case Payments:
		return r.payment.TIN
	}
	return r.taxpayer.TIN
}

// dedupKey is the natural key for intra-batch duplicate detection.
func dedupKey(r stagedRow) string {
	switch r.entity {
	case Taxpayers:
		return "tp|" + r.taxpayer.TIN
	case Filings:
		return "fl|" + r.filing.TIN + "|" + r.filing.Period + "|" + r.filing.TaxType
	case Payments:
		return "pm|" + r.payment.Reference
	}
	return ""
}

func decodeRow(entity EntityKind, m map[string]string) (stagedRow, error) {
	get := func(k string) string { return strings.TrimSpace(m[k]) }
	switch entity {
	case Taxpayers:
		r := &TaxpayerRow{TIN: get("tin"), Name: get("name"), Type: get("type"),
			RegistrationDate: get("registration_date"), Status: get("status")}
		return stagedRow{entity: entity, taxpayer: r}, nil
	case Filings:
		kobo, err := parseKobo(m["assessed_kobo"])
		if err != nil {
			return stagedRow{}, fmt.Errorf("assessed_kobo: %w", err)
		}
		return stagedRow{entity: entity, filing: &FilingRow{
			TIN: get("tin"), Period: get("period"), TaxType: get("tax_type"), AssessedKobo: kobo}}, nil
	case Payments:
		kobo, err := parseKobo(m["amount_kobo"])
		if err != nil {
			return stagedRow{}, fmt.Errorf("amount_kobo: %w", err)
		}
		return stagedRow{entity: entity, payment: &PaymentRow{
			TIN: get("tin"), Reference: get("reference"), AmountKobo: kobo, Date: get("date"), Channel: get("channel")}}, nil
	}
	return stagedRow{}, fmt.Errorf("unknown entity %q", entity)
}

func validateRow(entity EntityKind, sr stagedRow, strictChecksum bool) error {
	checkTIN := func(tin string) error {
		if !ValidTINFormat(tin) {
			return fmt.Errorf("tin %q fails format rule NNNNNNNN-NNNN", tin)
		}
		if strictChecksum && !TINChecksumOK(tin) {
			return fmt.Errorf("tin %q fails check-digit rule", tin)
		}
		return nil
	}
	switch entity {
	case Taxpayers:
		r := sr.taxpayer
		if err := checkTIN(r.TIN); err != nil {
			return err
		}
		if r.Name == "" {
			return fmt.Errorf("name required")
		}
		if r.Type != "individual" && r.Type != "company" {
			return fmt.Errorf("type must be individual|company")
		}
		if !validDate(r.RegistrationDate) {
			return fmt.Errorf("registration_date %q must be YYYY-MM-DD", r.RegistrationDate)
		}
		if r.Status == "" {
			return fmt.Errorf("status required")
		}
	case Filings:
		r := sr.filing
		if err := checkTIN(r.TIN); err != nil {
			return err
		}
		if !validPeriod(r.Period) {
			return fmt.Errorf("period %q must be YYYY-MM", r.Period)
		}
		if r.TaxType == "" {
			return fmt.Errorf("tax_type required")
		}
	case Payments:
		r := sr.payment
		if err := checkTIN(r.TIN); err != nil {
			return err
		}
		if r.Reference == "" {
			return fmt.Errorf("reference required")
		}
		if !validDate(r.Date) {
			return fmt.Errorf("date %q must be YYYY-MM-DD", r.Date)
		}
		if r.Channel == "" {
			return fmt.Errorf("channel required")
		}
	}
	return nil
}

const hashChainSeed = "meridian-mig-v1"

// buildManifest computes the content-addressed manifest over valid rows.
func buildManifest(rows []stagedRow, errs []RowError, opts Options, notes []string, now time.Time) *Manifest {
	counts := map[string]int{string(Taxpayers): 0, string(Filings): 0, string(Payments): 0, "rejected": len(errs)}
	sums := map[string]int64{"assessed_kobo": 0, "payments_kobo": 0}
	payload := sha256.New()
	payload.Write([]byte(hashChainSeed))
	chain := sha256.Sum256([]byte(hashChainSeed))
	rowHashes := make([]string, 0, len(rows))
	for _, r := range rows {
		canon := r.canonical()
		payload.Write(canon)
		chain = sha256.Sum256(append(chain[:], canon...))
		rowHashes = append(rowHashes, hex.EncodeToString(chain[:]))
		counts[string(r.entity)]++
		switch r.entity {
		case Filings:
			sums["assessed_kobo"] += r.filing.AssessedKobo
		case Payments:
			sums["payments_kobo"] += r.payment.AmountKobo
		}
	}
	payloadDigest := hex.EncodeToString(payload.Sum(nil))
	batchSum := sha256.Sum256([]byte("batch:" + payloadDigest))
	root := hex.EncodeToString(chain[:])
	if len(rowHashes) > 0 {
		root = rowHashes[len(rowHashes)-1]
	}
	return &Manifest{
		Version:       1,
		BatchID:       "migb-" + hex.EncodeToString(batchSum[:16]),
		Mode:          opts.Mode,
		Counts:        counts,
		SumsKobo:      sums,
		SHA256:        payloadDigest,
		HashChainRoot: root,
		RowHashes:     rowHashes,
		RowCount:      len(rows),
		Notes:         notes,
		CreatedAt:     now.UTC().Format(time.RFC3339),
	}
}

// docID returns the content-addressed document id for a staged row.
func docID(batchID string, r stagedRow) string {
	sum := sha256.Sum256(r.canonical())
	return batchID + "-" + hex.EncodeToString(sum[:8])
}

func collFor(e EntityKind) string { return "mig_" + string(e) }

type storedDoc struct {
	BatchID  string          `json:"batch_id"`
	RowIndex int             `json:"row_index"`
	RowHash  string          `json:"row_hash"`
	Entity   EntityKind      `json:"entity"`
	Row      json.RawMessage `json:"row"`
}

// commitBatch writes all docs as one batch transaction: any write failure
// rolls back every doc already written (no partial-file corruption).
func commitBatch(st Store, m *Manifest, rows []stagedRow) error {
	written := make([][2]string, 0, len(rows))
	rollback := func(cause error) error {
		for _, cw := range written {
			_ = st.Delete(cw[0], cw[1])
		}
		return fmt.Errorf("batch transaction failed, rolled back %d docs: %w", len(written), cause)
	}
	for i, r := range rows {
		rawRow := mustJSON(rowPayload(r))
		doc := storedDoc{BatchID: m.BatchID, RowIndex: i, RowHash: m.RowHashes[i], Entity: r.entity, Row: rawRow}
		b, err := json.Marshal(doc)
		if err != nil {
			return rollback(err)
		}
		coll, id := collFor(r.entity), docID(m.BatchID, r)
		if err := st.Put(coll, id, b); err != nil {
			return rollback(err)
		}
		written = append(written, [2]string{coll, id})
	}
	return nil
}

func rowPayload(r stagedRow) any {
	switch r.entity {
	case Taxpayers:
		return r.taxpayer
	case Filings:
		return r.filing
	default:
		return r.payment
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func putJSON(st Store, coll, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return st.Put(coll, id, b)
}

// --- verification (post-import proof) ---

// Verify re-computes counts, kobo sums and the per-row hash chain from
// the live store and emits a PASS/FAIL proof against the manifest.
// Signature integrity (when key provided) is checked first — a tampered
// manifest fails closed.
func Verify(st Store, m *Manifest, signingKey []byte, now time.Time) *Proof {
	var checks []Check
	add := func(name string, expected, actual any, ok bool) {
		checks = append(checks, Check{Name: name, Expected: fmt.Sprint(expected), Actual: fmt.Sprint(actual), OK: ok})
	}
	if signingKey != nil {
		add("manifest_signature", "valid HMAC-SHA256", map[bool]string{true: "valid", false: "INVALID"}[VerifySignature(m, signingKey)], VerifySignature(m, signingKey))
	}

	// gather committed docs for this batch, ordered by row index
	var docs []storedDoc
	for _, e := range []EntityKind{Taxpayers, Filings, Payments} {
		raws, err := st.List(collFor(e))
		if err != nil {
			add("store_read", "ok", err.Error(), false)
			return finishProof(m, checks, now)
		}
		for _, raw := range raws {
			var d storedDoc
			if json.Unmarshal(raw, &d) == nil && d.BatchID == m.BatchID {
				docs = append(docs, d)
			}
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].RowIndex < docs[j].RowIndex })

	counts := map[string]int{string(Taxpayers): 0, string(Filings): 0, string(Payments): 0}
	sums := map[string]int64{"assessed_kobo": 0, "payments_kobo": 0}
	chain := sha256.Sum256([]byte(hashChainSeed))
	payload := sha256.New()
	payload.Write([]byte(hashChainSeed))
	chainOK := true
	for i, d := range docs {
		counts[string(d.Entity)]++
		canon := canonicalStored(d)
		payload.Write(canon)
		chain = sha256.Sum256(append(chain[:], canon...))
		link := hex.EncodeToString(chain[:])
		if i >= len(m.RowHashes) || m.RowHashes[i] != link || d.RowHash != link {
			chainOK = false
		}
		switch d.Entity {
		case Filings:
			var r FilingRow
			_ = json.Unmarshal(d.Row, &r)
			sums["assessed_kobo"] += r.AssessedKobo
		case Payments:
			var r PaymentRow
			_ = json.Unmarshal(d.Row, &r)
			sums["payments_kobo"] += r.AmountKobo
		}
	}
	add("row_count", m.RowCount, len(docs), m.RowCount == len(docs))
	for _, e := range []EntityKind{Taxpayers, Filings, Payments} {
		add("count_"+string(e), m.Counts[string(e)], counts[string(e)], m.Counts[string(e)] == counts[string(e)])
	}
	add("sum_assessed_kobo", m.SumsKobo["assessed_kobo"], sums["assessed_kobo"], m.SumsKobo["assessed_kobo"] == sums["assessed_kobo"])
	add("sum_payments_kobo", m.SumsKobo["payments_kobo"], sums["payments_kobo"], m.SumsKobo["payments_kobo"] == sums["payments_kobo"])
	add("payload_sha256", m.SHA256, hex.EncodeToString(payload.Sum(nil)), m.SHA256 == hex.EncodeToString(payload.Sum(nil)))
	root := hex.EncodeToString(chain[:])
	add("hash_chain_root", m.HashChainRoot, root, m.HashChainRoot == root && chainOK)
	add("per_row_hash_chain", "intact", map[bool]string{true: "intact", false: "BROKEN"}[chainOK], chainOK)
	return finishProof(m, checks, now)
}

// canonicalStored rebuilds the canonical row bytes from a stored doc
// (same layout as stagedRow.canonical).
func canonicalStored(d storedDoc) []byte {
	norm := mustJSON(rowPayload(stagedRow{entity: d.Entity,
		taxpayer: decodeTaxpayer(d), filing: decodeFiling(d), payment: decodePayment(d)}))
	return append([]byte(`{"entity":"`+d.Entity+`","row":`), append(norm, '}')...)
}

func decodeTaxpayer(d storedDoc) *TaxpayerRow {
	if d.Entity != Taxpayers {
		return nil
	}
	var r TaxpayerRow
	_ = json.Unmarshal(d.Row, &r)
	return &r
}
func decodeFiling(d storedDoc) *FilingRow {
	if d.Entity != Filings {
		return nil
	}
	var r FilingRow
	_ = json.Unmarshal(d.Row, &r)
	return &r
}
func decodePayment(d storedDoc) *PaymentRow {
	if d.Entity != Payments {
		return nil
	}
	var r PaymentRow
	_ = json.Unmarshal(d.Row, &r)
	return &r
}

func finishProof(m *Manifest, checks []Check, now time.Time) *Proof {
	verdict := "PASS"
	for _, c := range checks {
		if !c.OK {
			verdict = "FAIL"
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "reconciliation proof for batch %s: %s\n", m.BatchID, verdict)
	fmt.Fprintf(&b, "  rows: %d committed (%d taxpayers, %d filings, %d payments, %d rejected at ingest)\n",
		m.RowCount, m.Counts[string(Taxpayers)], m.Counts[string(Filings)], m.Counts[string(Payments)], m.Counts["rejected"])
	fmt.Fprintf(&b, "  sums: assessed=%d kobo, payments=%d kobo\n", m.SumsKobo["assessed_kobo"], m.SumsKobo["payments_kobo"])
	for _, c := range checks {
		mark := "ok"
		if !c.OK {
			mark = "MISMATCH"
		}
		fmt.Fprintf(&b, "  - %-22s %-9s expected=%s actual=%s\n", c.Name, mark, c.Expected, c.Actual)
	}
	return &Proof{BatchID: m.BatchID, Verdict: verdict, Checks: checks, Summary: b.String(), CreatedAt: now.UTC().Format(time.RFC3339)}
}
