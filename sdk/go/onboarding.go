package meridian

import (
	"context"
	"fmt"
)

// OnboardingClient wraps services/onboarding (api/onboarding.yaml).
type OnboardingClient struct{ c *Client }

// Onboarding returns an onboarding client over the shared core.
func (c *Client) Onboarding() *OnboardingClient { return &OnboardingClient{c} }

type OperatorCreate struct {
	NIN           string `json:"nin"`
	FullName      string `json:"full_name"`
	Phone         string `json:"phone,omitempty"`
	State         string `json:"state,omitempty"`
	LGA           string `json:"lga,omitempty"`
	TradeCategory string `json:"trade_category,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
}

type DocRef struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Filename  string `json:"filename"`
	ObjectKey string `json:"object_key"`
	SHA256    string `json:"sha256,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Status    string `json:"status"`
	WORM      bool   `json:"worm"`
}

type Operator struct {
	ID            string   `json:"id"`
	NINHash       string   `json:"nin_hash"`
	TIN           string   `json:"tin,omitempty"`
	TINHash       string   `json:"tin_hash,omitempty"`
	FullName      string   `json:"full_name"`
	Phone         string   `json:"phone"`
	State         string   `json:"state"`
	LGA           string   `json:"lga"`
	TradeCategory string   `json:"trade_category"`
	Status        string   `json:"status"`
	ReviewStatus  string   `json:"review_status,omitempty"`
	Documents     []DocRef `json:"documents,omitempty"`
	AgentID       string   `json:"agent_id"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

type WorkflowRun struct {
	ID       string   `json:"id"`
	Workflow string   `json:"workflow"`
	Steps    []string `json:"steps"`
	Status   string   `json:"status"`
	Error    string   `json:"error,omitempty"`
	Result   any      `json:"result,omitempty"`
	Attempt  int      `json:"attempt"`
}

type OnboardingStatus struct {
	OperatorID   string   `json:"operator_id"`
	Status       string   `json:"status"`
	CurrentStep  string   `json:"current_step"`
	MissingItems []string `json:"missing_items"`
	Documents    []DocRef `json:"documents"`
	ReviewStatus string   `json:"review_status,omitempty"`
	TINHash      string   `json:"tin_hash,omitempty"`
	NextActions  []string `json:"next_actions"`
}

type Agent struct {
	ID            string `json:"id"`
	FullName      string `json:"full_name"`
	Phone         string `json:"phone"`
	LicenseNo     string `json:"license_no,omitempty"`
	State         string `json:"state"`
	LGA           string `json:"lga"`
	AssociationID string `json:"association_id,omitempty"`
	VettingStatus string `json:"vetting_status"`
}

type PresignResult struct {
	DocID     string `json:"doc_id"`
	UploadURL string `json:"upload_url"`
	Method    string `json:"method"`
	ExpiresAt string `json:"expires_at"`
	Backend   string `json:"backend"`
}

// CreateOperator registers an operator (idempotency key optional).
func (o *OnboardingClient) CreateOperator(ctx context.Context, in OperatorCreate, idempotencyKey string) (*Operator, error) {
	var op Operator
	if err := o.c.post(ctx, "/v1/operators", in, &op, idempotencyKey); err != nil {
		return nil, err
	}
	return &op, nil
}

// GetOperator fetches one operator.
func (o *OnboardingClient) GetOperator(ctx context.Context, id string) (*Operator, error) {
	var op Operator
	if err := o.c.get(ctx, "/v1/operators/"+id, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

// TransitionStatus applies a lifecycle transition (409 Problem on illegal).
func (o *OnboardingClient) TransitionStatus(ctx context.Context, id, to, reason string) (*Operator, error) {
	var op Operator
	body := map[string]string{"to": to, "reason": reason}
	if err := o.c.post(ctx, "/v1/operators/"+id+"/status", body, &op, ""); err != nil {
		return nil, err
	}
	return &op, nil
}

// ProvisionTIN runs wf-onb-tin-provision.
func (o *OnboardingClient) ProvisionTIN(ctx context.Context, operatorID, nin string) (*WorkflowRun, error) {
	var run WorkflowRun
	body := map[string]string{"operator_id": operatorID, "nin": nin}
	if err := o.c.post(ctx, "/v1/tin/provision", body, &run, ""); err != nil {
		return nil, err
	}
	return &run, nil
}

// RedriveRun re-drives a failed/crashed workflow run idempotently.
func (o *OnboardingClient) RedriveRun(ctx context.Context, runID string) (*WorkflowRun, error) {
	var run WorkflowRun
	if err := o.c.post(ctx, "/v1/workflows/runs/"+runID+"/redrive", nil, &run, ""); err != nil {
		return nil, err
	}
	return &run, nil
}

// Resumption returns the resumption view (current step + missing items).
func (o *OnboardingClient) Resumption(ctx context.Context, operatorID string) (*OnboardingStatus, error) {
	var st OnboardingStatus
	if err := o.c.get(ctx, "/v1/onboarding/"+operatorID, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// RegisterAgent registers a field agent (vetting=pending).
func (o *OnboardingClient) RegisterAgent(ctx context.Context, in Agent) (*Agent, error) {
	in.ID = ""
	in.VettingStatus = ""
	var ag Agent
	if err := o.c.post(ctx, "/v1/agents", in, &ag, ""); err != nil {
		return nil, err
	}
	return &ag, nil
}

// SetAgentVetting moves an agent through vetting (approved|suspended|rejected|pending).
func (o *OnboardingClient) SetAgentVetting(ctx context.Context, id, status, notes string) (*Agent, error) {
	var ag Agent
	body := map[string]string{"status": status, "notes": notes}
	if err := o.c.post(ctx, fmt.Sprintf("/v1/agents/%s/vetting", id), body, &ag, ""); err != nil {
		return nil, err
	}
	return &ag, nil
}

// PresignDocument gets a WORM upload URL for a document kind.
func (o *OnboardingClient) PresignDocument(ctx context.Context, operatorID, kind, filename string) (*PresignResult, error) {
	var res PresignResult
	body := map[string]string{"kind": kind, "filename": filename}
	if err := o.c.post(ctx, fmt.Sprintf("/v1/operators/%s/documents/presign", operatorID), body, &res, ""); err != nil {
		return nil, err
	}
	return &res, nil
}

// CompleteDocument attaches an uploaded document to the record.
func (o *OnboardingClient) CompleteDocument(ctx context.Context, operatorID, docID, sha256 string, size int64) (*DocRef, error) {
	var doc DocRef
	body := map[string]any{"doc_id": docID, "sha256": sha256, "size_bytes": size}
	if err := o.c.post(ctx, fmt.Sprintf("/v1/operators/%s/documents/complete", operatorID), body, &doc, ""); err != nil {
		return nil, err
	}
	return &doc, nil
}

// ReviewQueue lists records pending review.
func (o *OnboardingClient) ReviewQueue(ctx context.Context) ([]Operator, error) {
	var out struct {
		Queue []Operator `json:"queue"`
	}
	if err := o.c.get(ctx, "/v1/review/queue", &out); err != nil {
		return nil, err
	}
	return out.Queue, nil
}

// ReviewApprove approves a pending record (requires admin/operator role).
func (o *OnboardingClient) ReviewApprove(ctx context.Context, operatorID string) (*Operator, error) {
	var op Operator
	if err := o.c.post(ctx, "/v1/review/"+operatorID+"/approve", nil, &op, ""); err != nil {
		return nil, err
	}
	return &op, nil
}

// ReviewReject rejects a pending record (requires admin/operator role).
func (o *OnboardingClient) ReviewReject(ctx context.Context, operatorID string) (*Operator, error) {
	var op Operator
	if err := o.c.post(ctx, "/v1/review/"+operatorID+"/reject", nil, &op, ""); err != nil {
		return nil, err
	}
	return &op, nil
}
