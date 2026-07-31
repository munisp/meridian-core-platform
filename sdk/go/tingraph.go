package meridian

import "context"

// TinGraphClient wraps services/tin-graph (api/tin-graph.yaml).
type TinGraphClient struct{ c *Client }

// TinGraph returns a tin-graph client over the shared core.
func (c *Client) TinGraph() *TinGraphClient { return &TinGraphClient{c} }

type PersonRef struct {
	Name     string `json:"name"`
	DOB      string `json:"dob,omitempty"`
	NINHash  string `json:"nin_hash,omitempty"`
	IDDocRef string `json:"id_doc_ref,omitempty"`
}

type Director struct {
	PersonRef
	Role         string  `json:"role,omitempty"`
	SharePercent float64 `json:"share_percent,omitempty"`
}

type Shareholder struct {
	PersonRef
	SharePercent float64 `json:"share_percent"`
	ViaEntity    string  `json:"via_entity,omitempty"`
}

type UBO struct {
	PersonRef
	SharePercent float64 `json:"share_percent"`
	ViaEntity    string  `json:"via_entity,omitempty"`
	Source       string  `json:"source,omitempty"`
}

type CompanyProfile struct {
	CompanyName       string        `json:"company_name"`
	RCNumber          string        `json:"rc_number"`
	IncorporationDate string        `json:"incorporation_date,omitempty"`
	RegisteredAddress string        `json:"registered_address,omitempty"`
	ShareCapitalKobo  uint64        `json:"share_capital_kobo,omitempty"`
	Status            string        `json:"status,omitempty"`
	Directors         []Director    `json:"directors,omitempty"`
	Shareholders      []Shareholder `json:"shareholders,omitempty"`
}

type Entity struct {
	ID                 string        `json:"id"`
	TIN                string        `json:"tin,omitempty"`
	TINHash            string        `json:"tin_hash"`
	NINHash            string        `json:"nin_hash,omitempty"`
	CACRC              string        `json:"cac_rc,omitempty"`
	EntityType         string        `json:"entity_type"`
	Name               string        `json:"name"`
	Address            string        `json:"address,omitempty"`
	Directors          []Director    `json:"directors,omitempty"`
	Shareholders       []Shareholder `json:"shareholders,omitempty"`
	UBOs               []UBO         `json:"ubos,omitempty"`
	RegistryCrossCheck string        `json:"registry_cross_check,omitempty"`
	CreatedAt          string        `json:"created_at"`
}

type ProvisionRequest struct {
	NIN        string          `json:"nin,omitempty"`
	CACRC      string          `json:"cac_rc,omitempty"`
	EntityType string          `json:"entity_type,omitempty"`
	Name       string          `json:"name,omitempty"`
	Phone      string          `json:"phone,omitempty"`
	Email      string          `json:"email,omitempty"`
	Address    string          `json:"address,omitempty"`
	Company    *CompanyProfile `json:"company,omitempty"`
}

type ProvisionResult struct {
	Entity  Entity `json:"entity"`
	TIN     string `json:"tin"`
	TINHash string `json:"tin_hash"`
	UBOs    []UBO  `json:"ubos,omitempty"`
}

type UBOView struct {
	EntityID           string     `json:"entity_id"`
	EntityType         string     `json:"entity_type"`
	CACRC              string     `json:"cac_rc"`
	UBOs               []UBO      `json:"ubos"`
	Directors          []Director `json:"directors"`
	UboThresholdPct    float64    `json:"ubo_threshold_percent"`
	RegistryCrossCheck string     `json:"registry_cross_check"`
}

// ProvisionTIN fuses NIN=TIN / CAC-RC=TIN (idempotent).
func (t *TinGraphClient) ProvisionTIN(ctx context.Context, in ProvisionRequest) (*ProvisionResult, error) {
	var out ProvisionResult
	if err := t.c.post(ctx, "/v1/tin/provision", in, &out, ""); err != nil {
		return nil, err
	}
	return &out, nil
}

// VerifyTIN checks a TIN exists.
func (t *TinGraphClient) VerifyTIN(ctx context.Context, tin string) (bool, error) {
	var out struct {
		Valid bool `json:"valid"`
	}
	if err := t.c.post(ctx, "/v1/verify/tin", map[string]string{"tin": tin}, &out, ""); err != nil {
		return false, err
	}
	return out.Valid, nil
}

// EntityUBOs returns the directors + >25% UBO view for a company entity.
func (t *TinGraphClient) EntityUBOs(ctx context.Context, entityID string) (*UBOView, error) {
	var out UBOView
	if err := t.c.get(ctx, "/v1/entities/"+entityID+"/ubos", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateKYB attaches/refreshes a KYB profile (UBOs re-derived server-side).
func (t *TinGraphClient) UpdateKYB(ctx context.Context, entityID string, cp CompanyProfile) (*Entity, error) {
	var out struct {
		Entity Entity `json:"entity"`
	}
	if err := t.c.post(ctx, "/v1/entities/"+entityID+"/kyb", cp, &out, ""); err != nil {
		return nil, err
	}
	return &out.Entity, nil
}
