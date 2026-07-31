package graph

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Business KYB data model (audit O1/O7): directors and ultimate beneficial
// owners on the company entity. Raw ID documents stay as references (doc ids
// from the onboarding document store); only pseudonymised NIN hashes are
// held here per SPEC 1.3.
type PersonRef struct {
	Name     string `json:"name"`
	DOB      string `json:"dob,omitempty"`      // YYYY-MM-DD
	NINHash  string `json:"nin_hash,omitempty"` // pseudonymised; raw NIN never stored
	IDDocRef string `json:"id_doc_ref,omitempty"`
}

// Director is a company director captured at KYB.
type Director struct {
	PersonRef
	Role         string  `json:"role"` // director|company_secretary|chairman
	SharePercent float64 `json:"share_percent,omitempty"`
}

// UBO is an ultimate beneficial owner (natural person > 25% share/control
// per CBN AML/CFT rules; threshold below).
type UBO struct {
	PersonRef
	SharePercent float64 `json:"share_percent"`
	ViaEntity    string  `json:"via_entity,omitempty"` // holding company when indirect
	Source       string  `json:"source"`               // declared|derived
}

// UBOThresholdPercent is the CBN beneficial-ownership threshold (>25%).
const UBOThresholdPercent = 25.0

// Shareholder is a declared ownership line at CAC provision.
type Shareholder struct {
	PersonRef
	SharePercent float64 `json:"share_percent"`
	ViaEntity    string  `json:"via_entity,omitempty"`
}

// CompanyProfile is the full KYB profile accepted at CAC provision.
type CompanyProfile struct {
	CompanyName        string        `json:"company_name"`
	RCNumber           string        `json:"rc_number"`
	IncorporationDate  string        `json:"incorporation_date,omitempty"`
	RegisteredAddress  string        `json:"registered_address,omitempty"`
	ShareCapitalKobo   uint64        `json:"share_capital_kobo,omitempty"`
	Status             string        `json:"status,omitempty"` // active|dormant|struck_off
	Directors          []Director    `json:"directors,omitempty"`
	Shareholders       []Shareholder `json:"shareholders,omitempty"`
	RegistryCrossCheck string        `json:"registry_cross_check,omitempty"` // cac-simulator [simulated]|cac_api
}

// DeriveUBOs computes UBOs from declared shareholders: any natural person
// with share > 25% (direct or via a holding entity) is a UBO. Declared UBO
// records from the registry are merged (source=declared wins on same
// nin_hash).
func DeriveUBOs(shareholders []Shareholder, declared []UBO) []UBO {
	out := []UBO{}
	seen := map[string]bool{}
	for _, u := range declared {
		key := u.NINHash
		if key == "" {
			key = strings.ToLower(u.Name) + "|" + u.DOB
		}
		seen[key] = true
		u.Source = "declared"
		out = append(out, u)
	}
	for _, sh := range shareholders {
		if sh.SharePercent <= UBOThresholdPercent {
			continue
		}
		key := sh.NINHash
		if key == "" {
			key = strings.ToLower(sh.Name) + "|" + sh.DOB
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, UBO{
			PersonRef:    sh.PersonRef,
			SharePercent: sh.SharePercent,
			ViaEntity:    sh.ViaEntity,
			Source:       "derived",
		})
	}
	return out
}

// --- Real CAC HTTP adapter (mirror of the NIMC adapter pattern; HMAC-signed) ---

// CACHTTPAdapter verifies RC numbers against the real CAC public-search API.
// SIM-tagged sibling CACSimulator remains the dev default; prod wires this
// via CAC_API_URL (+CAC_API_KEY) and startup refuses without it (O8).
type CACHTTPAdapter struct {
	base   string
	apiKey string
	hc     *http.Client
}

// NewCACHTTPAdapter builds the adapter.
func NewCACHTTPAdapter(base, apiKey string) *CACHTTPAdapter {
	return &CACHTTPAdapter{base: strings.TrimRight(base, "/"), apiKey: apiKey, hc: &http.Client{Timeout: 10 * time.Second}}
}

func cacHMAC(key, value string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify implements CACAdapter against the real registry API.
func (a *CACHTTPAdapter) Verify(rc string) (CACVerification, error) {
	norm := strings.ToUpper(strings.TrimSpace(rc))
	body, _ := json.Marshal(map[string]string{"rc_number": norm})
	req, err := http.NewRequest(http.MethodPost, a.base+"/verify", bytes.NewReader(body))
	if err != nil {
		return CACVerification{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CAC-Signature", cacHMAC(a.apiKey, string(body)))
	if a.apiKey != "" {
		req.Header.Set("X-API-Key", a.apiKey)
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return CACVerification{}, fmt.Errorf("cac adapter: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return CACVerification{}, fmt.Errorf("cac adapter: status %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Valid    bool   `json:"valid"`
		Verified bool   `json:"verified"`
		Company  string `json:"company_name"`
		Status   string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CACVerification{}, fmt.Errorf("cac adapter: decode: %w", err)
	}
	return CACVerification{
		RCNumber: norm, Valid: out.Valid, Verified: out.Verified,
		Company: out.Company, Status: out.Status, Provider: "cac_api",
	}, nil
}

// NINHTTPAdapter verifies NINs against the real NIMC API (prod wiring for
// the tin-graph NIN adapter; mirrors onboarding's NIMCHTTPAdapter).
type NINHTTPAdapter struct {
	base   string
	apiKey string
	hc     *http.Client
}

// NewNINHTTPAdapter builds the adapter.
func NewNINHTTPAdapter(base, apiKey string) *NINHTTPAdapter {
	return &NINHTTPAdapter{base: strings.TrimRight(base, "/"), apiKey: apiKey, hc: &http.Client{Timeout: 10 * time.Second}}
}

// Verify implements NINAdapter against the real NIMC API.
func (a *NINHTTPAdapter) Verify(nin string) (NINVerification, error) {
	body, _ := json.Marshal(map[string]string{"nin": nin})
	req, err := http.NewRequest(http.MethodPost, a.base+"/verify", bytes.NewReader(body))
	if err != nil {
		return NINVerification{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-NIMC-Signature", cacHMAC(a.apiKey, string(body)))
	if a.apiKey != "" {
		req.Header.Set("X-API-Key", a.apiKey)
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return NINVerification{}, fmt.Errorf("nimc adapter: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return NINVerification{}, fmt.Errorf("nimc adapter: status %d: %s", resp.StatusCode, string(b))
	}
	var out NINVerification
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return NINVerification{}, fmt.Errorf("nimc adapter: decode: %w", err)
	}
	out.NIN = nin
	out.Provider = "nimc_api"
	return out, nil
}

// AdaptersFromEnv selects NIN/CAC adapters per profile (O8 fail-closed):
// PROFILE=prod requires NIMC_API_URL and CAC_API_URL — a missing var is a
// fatal misconfiguration (no silent simulator in prod). Dev keeps the
// deterministic simulators.
func AdaptersFromEnv(profile string) (NINAdapter, CACAdapter, error) {
	ninURL, cacURL := os.Getenv("NIMC_API_URL"), os.Getenv("CAC_API_URL")
	if ninURL != "" || cacURL != "" {
		if ninURL == "" || cacURL == "" {
			return nil, nil, fmt.Errorf("NIMC_API_URL and CAC_API_URL must be set together (partial prod config)")
		}
		return NewNINHTTPAdapter(ninURL, os.Getenv("NIMC_API_KEY")),
			NewCACHTTPAdapter(cacURL, os.Getenv("CAC_API_KEY")), nil
	}
	if profile == "prod" || profile == "production" {
		return nil, nil, fmt.Errorf("profile=prod FATAL: NIMC_API_URL and CAC_API_URL are required (refusing to start with registry simulators)")
	}
	return NINSimulator{}, CACSimulator{}, nil
}
