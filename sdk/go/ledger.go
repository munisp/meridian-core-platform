package meridian

import "context"

// LedgerClient wraps services/ledger (api/ledger.yaml). Amounts are kobo.
type LedgerClient struct{ c *Client }

// Ledger returns a ledger client over the shared core.
func (c *Client) Ledger() *LedgerClient { return &LedgerClient{c} }

type Account struct {
	ID       string `json:"id"`
	Ledger   uint64 `json:"ledger"`
	Code     uint16 `json:"code"`
	Flags    uint16 `json:"flags,omitempty"`
	UserData string `json:"user_data,omitempty"`
}

type Balance struct {
	AccountID      string `json:"account_id"`
	DebitsPending  uint64 `json:"debits_pending"`
	DebitsPosted   uint64 `json:"debits_posted"`
	CreditsPending uint64 `json:"credits_pending"`
	CreditsPosted  uint64 `json:"credits_posted"`
	PostedNet      int64  `json:"posted_net"`
	AvailableKobo  int64  `json:"available"`
}

type Transfer struct {
	ID              string `json:"id,omitempty"`
	DebitAccountID  string `json:"debit_account_id"`
	CreditAccountID string `json:"credit_account_id"`
	Amount          uint64 `json:"amount"`
	Ledger          uint64 `json:"ledger"`
	Code            uint16 `json:"code"`
	Pending         bool   `json:"pending,omitempty"`
	Resolved        bool   `json:"resolved,omitempty"`
	UserData        string `json:"user_data,omitempty"`
}

// CreateAccounts creates accounts (idempotency key recommended).
func (l *LedgerClient) CreateAccounts(ctx context.Context, accounts []Account, idempotencyKey string) error {
	return l.c.post(ctx, "/v1/accounts", map[string]any{"accounts": accounts}, nil, idempotencyKey)
}

// Balance returns the derived balance for an account.
func (l *LedgerClient) Balance(ctx context.Context, accountID string) (*Balance, error) {
	var b Balance
	if err := l.c.get(ctx, "/v1/accounts/"+accountID+"/balance", &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// Transfer posts a settled double-entry transfer.
func (l *LedgerClient) Transfer(ctx context.Context, t Transfer, idempotencyKey string) (*Transfer, error) {
	var out Transfer
	if err := l.c.post(ctx, "/v1/transfers", t, &out, idempotencyKey); err != nil {
		return nil, err
	}
	return &out, nil
}

// TransferPending creates a pending (two-phase) transfer.
func (l *LedgerClient) TransferPending(ctx context.Context, t Transfer, idempotencyKey string) (*Transfer, error) {
	var out Transfer
	if err := l.c.post(ctx, "/v1/transfers/pending", t, &out, idempotencyKey); err != nil {
		return nil, err
	}
	return &out, nil
}

// PostPending settles a pending transfer.
func (l *LedgerClient) PostPending(ctx context.Context, id string) (*Transfer, error) {
	var out Transfer
	if err := l.c.post(ctx, "/v1/transfers/"+id+"/post", nil, &out, ""); err != nil {
		return nil, err
	}
	return &out, nil
}

// VoidPending releases a pending transfer reservation.
func (l *LedgerClient) VoidPending(ctx context.Context, id string) (*Transfer, error) {
	var out Transfer
	if err := l.c.post(ctx, "/v1/transfers/"+id+"/void", nil, &out, ""); err != nil {
		return nil, err
	}
	return &out, nil
}
