// Package graph implements TIN provisioning, verification and entity
// resolution for the tin-graph service (SPEC 2).
//
// Pseudonymisation: tin_hash = HMAC-SHA256(tin, TIN_HMAC_KEY) (SPEC 1.3).
// NIN/CAC verification goes through adapter interfaces with deterministic
// dev simulators (SPEC: external rails behind interfaces + simulators).
package graph

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
)

// Entity is a resolved identity node. Raw PII never leaves this service;
// analytics planes only ever see the *_hash fields.
type Entity struct {
	ID         string            `json:"id"`
	TIN        string            `json:"tin,omitempty"`
	TINHash    string            `json:"tin_hash"`
	NINHash    string            `json:"nin_hash,omitempty"`
	CACRC      string            `json:"cac_rc,omitempty"`
	EntityType string            `json:"entity_type"` // individual|company
	Name       string            `json:"name"`
	Phone      string            `json:"phone,omitempty"`
	Email      string            `json:"email,omitempty"`
	Address    string            `json:"address,omitempty"`
	Attrs      map[string]string `json:"attrs,omitempty"`
	CreatedAt  string            `json:"created_at"`
}

// TINHMACKey from env (SPEC 1.3). A5 hardening: PROFILE=prod REQUIRES a
// dedicated TIN_HMAC_KEY — the dev default is never used in prod (the
// tin_hash pseudonymisation is only as strong as this key).
func TINHMACKey() []byte {
	k := os.Getenv("TIN_HMAC_KEY")
	if k == "" {
		if os.Getenv("PROFILE") == "prod" {
			log.Fatal("profile=prod FATAL: TIN_HMAC_KEY is required (no dev-secret default)")
		}
		k = "meridian-dev-tin-hmac-key"
	}
	return []byte(k)
}

// HashTIN computes the pseudonymised tin_hash.
func HashTIN(tin string) string {
	mac := hmac.New(sha256.New, TINHMACKey())
	mac.Write([]byte(tin))
	return hex.EncodeToString(mac.Sum(nil))
}

// HashValue is the generic pseudonymisation helper for NIN etc.
func HashValue(v string) string {
	mac := hmac.New(sha256.New, TINHMACKey())
	mac.Write([]byte("nin:" + v))
	return hex.EncodeToString(mac.Sum(nil))
}

var (
	ninRe = regexp.MustCompile(`^\d{11}$`)
	rcRe  = regexp.MustCompile(`^(RC|BN)?\d{5,8}$`)
)

// ProvisionTIN deterministically fuses NIN=TIN / CAC-RC=TIN (dev issuance:
// real issuance is an NRS adapter; the fusion rule is the real logic).
func ProvisionTIN(nin, cacRC string) (string, error) {
	seed := ""
	switch {
	case nin != "":
		if !ninRe.MatchString(nin) {
			return "", fmt.Errorf("NIN must be 11 digits")
		}
		seed = "nin:" + nin
	case cacRC != "":
		norm := strings.ToUpper(strings.TrimSpace(cacRC))
		if !rcRe.MatchString(norm) {
			return "", fmt.Errorf("CAC RC must match RC\\d{5,8} or BN\\d{5,8}")
		}
		seed = "cac:" + norm
	default:
		return "", fmt.Errorf("nin or cac_rc required")
	}
	sum := sha256.Sum256([]byte(seed))
	n := binary.BigEndian.Uint64(sum[:8]) % 100_000_000_000 // 11 digits max
	return fmt.Sprintf("%08d-%04d", n/10000, n%10000), nil
}

// --- Verification adapters (NIMC / CAC behind interfaces + simulators) ---

// NINVerification is the NIMC adapter result.
type NINVerification struct {
	NIN       string `json:"nin"`
	Valid     bool   `json:"valid"`
	Verified  bool   `json:"verified"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	DOB       string `json:"dob,omitempty"`
	Provider  string `json:"provider"` // nimc-simulator in dev
}

// NINAdapter verifies NINs against NIMC.
type NINAdapter interface {
	Verify(nin string) (NINVerification, error)
}

// NINSimulator is the deterministic dev NIMC simulator.
type NINSimulator struct{}

// Verify implements deterministic simulated verification.
func (NINSimulator) Verify(nin string) (NINVerification, error) {
	res := NINVerification{NIN: nin, Provider: "nimc-simulator [simulated]"}
	if !ninRe.MatchString(nin) {
		return res, nil
	}
	sum := sha256.Sum256([]byte("nimc:" + nin))
	first := []string{"Adaeze", "Tunde", "Chiamaka", "Ibrahim", "Ngozi", "Musa", "Yetunde", "Emeka"}
	last := []string{"Okafor", "Balogun", "Eze", "Abdullahi", "Adeyemi", "Danjuma", "Ogunleye", "Nwachukwu"}
	res.Valid = true
	res.Verified = sum[0]%100 != 0 // 1% deterministic mismatch rate
	res.FirstName = first[sum[1]%uint8(len(first))]
	res.LastName = last[sum[2]%uint8(len(last))]
	res.DOB = fmt.Sprintf("19%02d-%02d-%02d", 60+sum[3]%40, 1+sum[4]%12, 1+sum[5]%28)
	return res, nil
}

// CACVerification is the CAC adapter result.
type CACVerification struct {
	RCNumber string `json:"rc_number"`
	Valid    bool   `json:"valid"`
	Verified bool   `json:"verified"`
	Company  string `json:"company_name,omitempty"`
	Status   string `json:"status,omitempty"`
	Provider string `json:"provider"`
}

// CACAdapter verifies RC numbers against CAC.
type CACAdapter interface {
	Verify(rc string) (CACVerification, error)
}

// CACSimulator is the deterministic dev CAC simulator.
type CACSimulator struct{}

// Verify implements deterministic simulated verification.
func (CACSimulator) Verify(rc string) (CACVerification, error) {
	norm := strings.ToUpper(strings.TrimSpace(rc))
	res := CACVerification{RCNumber: norm, Provider: "cac-simulator [simulated]"}
	if !rcRe.MatchString(norm) {
		return res, nil
	}
	sum := sha256.Sum256([]byte("cac:" + norm))
	words1 := []string{"Meridian", "Lagos", "Sahel", "Niger", "Benue", "Atlantic", "Delta", "Kano"}
	words2 := []string{"Trading", "Logistics", "Farms", "Technologies", "Industries", "Ventures", "Commerce", "Holdings"}
	res.Valid = true
	res.Verified = sum[0]%100 != 0
	res.Company = fmt.Sprintf("%s %s %s", words1[sum[1]%8], words2[sum[2]%8], map[bool]string{true: "Ltd", false: "PLC"}[sum[3]%2 == 0])
	res.Status = "active"
	return res, nil
}

// --- Entity resolution (rp-identity-match-thresholds) ---

// MatchConfig mirrors the rp-identity-match-thresholds pack content.
type MatchConfig struct {
	AutoMatchThreshold float64            `json:"auto_match_threshold"`
	ReviewThreshold    float64            `json:"review_threshold"`
	Weights            map[string]float64 `json:"weights"`
}

// DefaultMatchConfig is the [seed] fallback of rp-identity-match-thresholds.
var DefaultMatchConfig = MatchConfig{
	AutoMatchThreshold: 0.85,
	ReviewThreshold:    0.60,
	Weights: map[string]float64{
		"name": 0.30, "phone": 0.25, "email": 0.20, "address": 0.10, "nin": 0.15,
	},
}

// Candidate is a scored entity-resolution candidate.
type Candidate struct {
	EntityID   string             `json:"entity_id"`
	TINHash    string             `json:"tin_hash"`
	Score      float64            `json:"score"`
	FieldScore map[string]float64 `json:"field_scores"`
}

// levenshtein distance (small strings; DP).
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func normStr(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// stringSimilarity is 1 - normLevenshtein on normalised strings.
func stringSimilarity(a, b string) float64 {
	a, b = normStr(a), normStr(b)
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	d := levenshtein(a, b)
	l := len([]rune(a))
	if len([]rune(b)) > l {
		l = len([]rune(b))
	}
	return 1 - float64(d)/float64(l)
}

func normPhone(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	// normalise Nigerian +234 prefix
	if strings.HasPrefix(s, "234") && len(s) == 13 {
		s = "0" + s[3:]
	}
	return s
}

// Attributes are the resolution input.
type Attributes struct {
	Name    string `json:"name"`
	Phone   string `json:"phone"`
	Email   string `json:"email"`
	Address string `json:"address"`
	NIN     string `json:"nin"`
	TIN     string `json:"tin"`
}

// ScoreCandidate computes the weighted match score of attrs vs entity.
func ScoreCandidate(cfg MatchConfig, attrs Attributes, e Entity) Candidate {
	fs := map[string]float64{}
	if attrs.Name != "" && e.Name != "" {
		fs["name"] = stringSimilarity(attrs.Name, e.Name)
	}
	if attrs.Phone != "" && e.Phone != "" {
		if normPhone(attrs.Phone) == normPhone(e.Phone) {
			fs["phone"] = 1
		} else {
			fs["phone"] = 0
		}
	}
	if attrs.Email != "" && e.Email != "" {
		if strings.EqualFold(normStr(attrs.Email), normStr(e.Email)) {
			fs["email"] = 1
		} else {
			fs["email"] = 0
		}
	}
	if attrs.Address != "" && e.Address != "" {
		fs["address"] = stringSimilarity(attrs.Address, e.Address)
	}
	if attrs.NIN != "" && e.NINHash != "" {
		if HashValue(attrs.NIN) == e.NINHash {
			fs["nin"] = 1
		} else {
			fs["nin"] = 0
		}
	}
	var score, wsum float64
	for field, w := range cfg.Weights {
		if s, ok := fs[field]; ok {
			score += w * s
			wsum += w
		}
	}
	if wsum > 0 {
		score /= wsum // normalise over comparable fields only
	}
	return Candidate{EntityID: e.ID, TINHash: e.TINHash, Score: score, FieldScore: fs}
}

// ResolveDecision is the resolution verdict from the pack thresholds.
type ResolveDecision string

const (
	DecisionAutoMatch ResolveDecision = "auto_match"
	DecisionReview    ResolveDecision = "review"
	DecisionNew       ResolveDecision = "new_entity"
)

// Resolve scores attrs against all entities and applies the pack thresholds.
func Resolve(cfg MatchConfig, attrs Attributes, entities []Entity) (ResolveDecision, []Candidate) {
	cands := make([]Candidate, 0, len(entities))
	for _, e := range entities {
		c := ScoreCandidate(cfg, attrs, e)
		if c.Score > 0 {
			cands = append(cands, c)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Score > cands[j].Score })
	if len(cands) > 0 {
		if cands[0].Score >= cfg.AutoMatchThreshold {
			return DecisionAutoMatch, cands
		}
		if cands[0].Score >= cfg.ReviewThreshold {
			return DecisionReview, cands
		}
	}
	return DecisionNew, cands
}

// Edge is a relationship between entities via shared attributes.
type Edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Rel    string `json:"rel"` // shared_phone|shared_email|shared_address|same_nin
	Detail string `json:"detail,omitempty"`
}

// GraphView is the entity neighbourhood response.
type GraphView struct {
	Root  string   `json:"root"`
	Nodes []Entity `json:"nodes"`
	Edges []Edge   `json:"edges"`
}

// BuildGraph computes shared-attribute edges for a root entity.
func BuildGraph(rootID string, entities []Entity) GraphView {
	byID := map[string]Entity{}
	for _, e := range entities {
		byID[e.ID] = e
	}
	root, ok := byID[rootID]
	gv := GraphView{Root: rootID}
	if !ok {
		return gv
	}
	gv.Nodes = append(gv.Nodes, root)
	seen := map[string]bool{rootID: true}
	addEdge := func(other Entity, rel, detail string) {
		gv.Edges = append(gv.Edges, Edge{From: rootID, To: other.ID, Rel: rel, Detail: detail})
		if !seen[other.ID] {
			seen[other.ID] = true
			gv.Nodes = append(gv.Nodes, other)
		}
	}
	for _, e := range entities {
		if e.ID == rootID {
			continue
		}
		if root.Phone != "" && normPhone(root.Phone) == normPhone(e.Phone) {
			addEdge(e, "shared_phone", "same normalised phone")
			continue
		}
		if root.Email != "" && strings.EqualFold(normStr(root.Email), normStr(e.Email)) {
			addEdge(e, "shared_email", "same email")
			continue
		}
		if root.Address != "" && stringSimilarity(root.Address, e.Address) == 1 {
			addEdge(e, "shared_address", "same address")
			continue
		}
		if root.NINHash != "" && root.NINHash == e.NINHash {
			addEdge(e, "same_nin", "same nin_hash")
		}
	}
	sort.Slice(gv.Nodes, func(i, j int) bool { return gv.Nodes[i].ID < gv.Nodes[j].ID })
	return gv
}

// NewEntityID derives a stable entity id.
func NewEntityID() string { return "ent-" + envelope.NewULID() }

// NowRFC3339 helper.
func NowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }
