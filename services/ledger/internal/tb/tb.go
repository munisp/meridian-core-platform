// Package tb implements TigerBeetle ledger semantics (SPEC 1.5) behind the
// LedgerClient interface. DevClient is a durable in-process implementation
// (double-entry, pending transfers, DEBITS_MUST_NOT_EXCEED_CREDITS) so the
// suite runs without a TigerBeetle cluster; a real client is selected when
// TIGERBEETLE_ADDRESSES is set.
//
// Account id = 128-bit: high 64 bits = namespace (ledger id), low 64 bits =
// entity serial. Transfer codes: 1=authorise(pending), 2=capture(post_pending),
// 3=void, 4=topup, 5=settle, 6=hold, 7=release. Amounts are integer kobo.
package tb

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Ledger ids (SPEC 1.5).
const (
	LedgerAgentFloat    uint64 = 100
	LedgerPSMPayments   uint64 = 200
	LedgerVATRemittance uint64 = 300
	LedgerPSSPRecon     uint64 = 400
	LedgerDisputeDep    uint64 = 500
	LedgerT11Attrib     uint64 = 600
	LedgerCommissions   uint64 = 700
)

// Transfer codes (SPEC 1.5).
const (
	CodeAuthorise uint16 = 1 // pending
	CodeCapture   uint16 = 2 // post_pending
	CodeVoid      uint16 = 3
	CodeTopup     uint16 = 4
	CodeSettle    uint16 = 5
	CodeHold      uint16 = 6 // pending hold
	CodeRelease   uint16 = 7 // release hold (void)
)

// Account flags.
const (
	FlagDebitsMustNotExceedCredits uint16 = 1 << 0
	FlagCreditsMustNotExceedDebits uint16 = 1 << 1
	FlagLinked                     uint16 = 1 << 2 // accepted, chains not required in dev
)

// ID is a 128-bit account/transfer identifier.
type ID struct {
	High uint64 `json:"high"`
	Low  uint64 `json:"low"`
}

// MakeID builds a 128-bit id from namespace and serial.
func MakeID(namespace, serial uint64) ID { return ID{High: namespace, Low: serial} }

// String renders the id as 32 lowercase hex chars (big-endian).
func (id ID) String() string {
	var b [16]byte
	for i := 0; i < 8; i++ {
		b[i] = byte(id.High >> (56 - 8*i))
		b[8+i] = byte(id.Low >> (56 - 8*i))
	}
	return hex.EncodeToString(b[:])
}

// ParseID parses a 32-hex-char id.
func ParseID(s string) (ID, error) {
	var id ID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return id, fmt.Errorf("invalid 128-bit id %q", s)
	}
	for i := 0; i < 8; i++ {
		id.High = id.High<<8 | uint64(b[i])
		id.Low = id.Low<<8 | uint64(b[8+i])
	}
	return id, nil
}

// Zero reports whether the id is unset.
func (id ID) Zero() bool { return id.High == 0 && id.Low == 0 }

// Account mirrors the TigerBeetle account balance model.
type Account struct {
	ID             ID     `json:"id"`
	Ledger         uint64 `json:"ledger"`
	Code           uint16 `json:"code"`
	Flags          uint16 `json:"flags"`
	UserData       string `json:"user_data,omitempty"` // metadata key into Postgres, never PII
	DebitsPending  uint64 `json:"debits_pending"`
	DebitsPosted   uint64 `json:"debits_posted"`
	CreditsPending uint64 `json:"credits_pending"`
	CreditsPosted  uint64 `json:"credits_posted"`
	CreatedAt      string `json:"created_at"`
}

// Balance is the derived spendable/available view.
type Balance struct {
	AccountID      ID     `json:"account_id"`
	DebitsPending  uint64 `json:"debits_pending"`
	DebitsPosted   uint64 `json:"debits_posted"`
	CreditsPending uint64 `json:"credits_pending"`
	CreditsPosted  uint64 `json:"credits_posted"`
	PostedNet      int64  `json:"posted_net"` // credits_posted - debits_posted (kobo)
	AvailableKobo  int64  `json:"available"`  // credits_posted - debits_posted - debits_pending
}

// Transfer is a double-entry transfer. Pending transfers reserve amounts;
// PostPending/VoidPending resolve them.
type Transfer struct {
	ID              ID     `json:"id"`
	DebitAccountID  ID     `json:"debit_account_id"`
	CreditAccountID ID     `json:"credit_account_id"`
	Amount          uint64 `json:"amount"` // kobo
	Ledger          uint64 `json:"ledger"`
	Code            uint16 `json:"code"`
	Pending         bool   `json:"pending"`
	PendingID       ID     `json:"pending_id,omitempty"`
	Resolved        bool   `json:"resolved,omitempty"` // pending transfer posted/voided
	UserData        string `json:"user_data,omitempty"`
	CreatedAt       string `json:"created_at"`
	// TimeoutSeconds bounds how long a pending transfer may stay unresolved
	// (TigerBeetle pending timeout). 0 = no expiry. The sweeper voids
	// unresolved pendings whose ExpiresAt has passed.
	TimeoutSeconds uint32 `json:"timeout_seconds,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"` // RFC3339Nano, set on pending creation
}

// ResultCode mirrors TigerBeetle result codes (representative subset).
type ResultCode string

const (
	OK                              ResultCode = "ok"
	Exists                          ResultCode = "exists"
	ExistsWithDifferentAttributes   ResultCode = "exists_with_different_attributes"
	AccountNotFound                 ResultCode = "account_not_found"
	AccountsMustBeDifferent         ResultCode = "accounts_must_be_different"
	AmountMustBePositive            ResultCode = "amount_must_be_positive"
	ExceedsCredits                  ResultCode = "exceeds_credits"
	ExceedsDebits                   ResultCode = "exceeds_debits"
	LedgerMustMatch                 ResultCode = "accounts_must_have_the_same_ledger"
	PendingTransferNotFound         ResultCode = "pending_transfer_not_found"
	PendingTransferNotPending       ResultCode = "pending_transfer_not_pending"
	ExceedsPendingAmount            ResultCode = "exceeds_pending_amount"
	PendingTransferHasDifferentAttr ResultCode = "pending_transfer_has_different_attributes"
	TransferNotPending              ResultCode = "transfer_not_pending"
	Overflows                       ResultCode = "overflows"
)

// Result is the outcome of a single operation.
type Result struct {
	Code    ResultCode `json:"code"`
	Message string     `json:"message,omitempty"`
}

// ErrRealClientRequired is returned by the placeholder real client when
// TIGERBEETLE_ADDRESSES is set but the native client is not compiled in.
var ErrRealClientRequired = errors.New("real TigerBeetle client requires the tigerbeetle-go build tag; use DevClient in dev")

// LedgerClient is the service-facing interface (SPEC 1.5).
type LedgerClient interface {
	CreateAccounts([]Account) ([]Result, error)
	Transfer(t Transfer) (Result, error)
	PendingTransfer(t Transfer) (Result, error)
	PostPending(pendingID ID, amount uint64, code uint16) (Result, error)
	VoidPending(pendingID ID, code uint16) (Result, error)
	Balance(accountID ID) (Balance, Result, error)
	GetAccount(id ID) (Account, Result, error)
	GetTransfer(id ID) (Transfer, Result, error)
	ListAccounts() ([]Account, error)
	ListTransfers(accountID ID) ([]Transfer, error)
}

// DevClient is the durable dev implementation of TigerBeetle semantics.
type DevClient struct {
	mu        sync.Mutex
	accounts  map[ID]*Account
	transfers map[ID]*Transfer
	nextSer   map[uint64]uint64 // per-namespace serial allocator
	onChange  func()            // persistence hook (atomic snapshot)
	onEvent   func(t Transfer)  // event hook (outbox emission)
}

// NewDevClient creates an empty dev client.
func NewDevClient() *DevClient {
	return &DevClient{
		accounts:  map[ID]*Account{},
		transfers: map[ID]*Transfer{},
		nextSer:   map[uint64]uint64{},
	}
}

// SetHooks wires persistence and event hooks.
func (c *DevClient) SetHooks(onChange func(), onEvent func(t Transfer)) {
	c.onChange = onChange
	c.onEvent = onEvent
}

// Snapshot exports state (for persistence).
func (c *DevClient) Snapshot() (accounts []Account, transfers []Transfer, serials map[uint64]uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range c.accounts {
		accounts = append(accounts, *a)
	}
	for _, t := range c.transfers {
		transfers = append(transfers, *t)
	}
	serials = map[uint64]uint64{}
	for k, v := range c.nextSer {
		serials[k] = v
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID.String() < accounts[j].ID.String() })
	sort.Slice(transfers, func(i, j int) bool { return transfers[i].ID.String() < transfers[j].ID.String() })
	return
}

// Restore loads a snapshot.
func (c *DevClient) Restore(accounts []Account, transfers []Transfer, serials map[uint64]uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range accounts {
		cp := a
		c.accounts[a.ID] = &cp
	}
	for _, t := range transfers {
		cp := t
		c.transfers[t.ID] = &cp
	}
	for k, v := range serials {
		c.nextSer[k] = v
	}
}

// NextSerial allocates the next serial in a namespace (ID generation aid).
func (c *DevClient) NextSerial(namespace uint64) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextSer[namespace]++
	return c.nextSer[namespace]
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// CreateAccounts creates accounts; per-account results align by index.
func (c *DevClient) CreateAccounts(accts []Account) ([]Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	results := make([]Result, len(accts))
	for i, a := range accts {
		if a.ID.Zero() {
			results[i] = Result{Code: AccountNotFound, Message: "id must be non-zero"}
			continue
		}
		if existing, ok := c.accounts[a.ID]; ok {
			if existing.Ledger == a.Ledger && existing.Flags == a.Flags && existing.Code == a.Code {
				results[i] = Result{Code: Exists}
			} else {
				results[i] = Result{Code: ExistsWithDifferentAttributes}
			}
			continue
		}
		a.CreatedAt = now()
		cp := a
		c.accounts[a.ID] = &cp
		if a.ID.Low > c.nextSer[a.ID.High] {
			c.nextSer[a.ID.High] = a.ID.Low
		}
		results[i] = Result{Code: OK}
	}
	c.changedLocked()
	return results, nil
}

func (c *DevClient) changedLocked() {
	if c.onChange != nil {
		c.onChange()
	}
}

func (c *DevClient) eventLocked(t Transfer) {
	if c.onEvent != nil {
		c.onEvent(t)
	}
}

// checkConstraints validates balance invariants for a mutation.
func checkConstraints(a *Account, addDebitsPending, addDebitsPosted, addCreditsPending, addCreditsPosted uint64) ResultCode {
	dp := a.DebitsPending + addDebitsPending
	dposted := a.DebitsPosted + addDebitsPosted
	cp := a.CreditsPending + addCreditsPending
	cposted := a.CreditsPosted + addCreditsPosted
	// overflow guards (uint64)
	if dp < a.DebitsPending || dposted < a.DebitsPosted || cp < a.CreditsPending || cposted < a.CreditsPosted {
		return Overflows
	}
	if a.Flags&FlagDebitsMustNotExceedCredits != 0 {
		if dp+dposted > cp+cposted {
			return ExceedsCredits
		}
	}
	if a.Flags&FlagCreditsMustNotExceedDebits != 0 {
		if cp+cposted > dp+dposted {
			return ExceedsDebits
		}
	}
	return OK
}

func (c *DevClient) validateParties(t *Transfer) (*Account, *Account, Result) {
	if t.Amount == 0 {
		return nil, nil, Result{Code: AmountMustBePositive}
	}
	if t.DebitAccountID == t.CreditAccountID {
		return nil, nil, Result{Code: AccountsMustBeDifferent}
	}
	dr, ok := c.accounts[t.DebitAccountID]
	if !ok {
		return nil, nil, Result{Code: AccountNotFound, Message: "debit account"}
	}
	cr, ok := c.accounts[t.CreditAccountID]
	if !ok {
		return nil, nil, Result{Code: AccountNotFound, Message: "credit account"}
	}
	if dr.Ledger != cr.Ledger {
		return nil, nil, Result{Code: LedgerMustMatch}
	}
	return dr, cr, Result{Code: OK}
}

// Transfer posts an immediate (non-pending) double-entry transfer.
func (c *DevClient) Transfer(t Transfer) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.transfers[t.ID]; ok {
		if sameAttrs(*existing, t) {
			return Result{Code: Exists}, nil
		}
		return Result{Code: ExistsWithDifferentAttributes}, nil
	}
	dr, cr, res := c.validateParties(&t)
	if res.Code != OK {
		return res, nil
	}
	if rc := checkConstraints(dr, 0, t.Amount, 0, 0); rc != OK {
		return Result{Code: rc, Message: "debit account"}, nil
	}
	if rc := checkConstraints(cr, 0, 0, 0, t.Amount); rc != OK {
		return Result{Code: rc, Message: "credit account"}, nil
	}
	dr.DebitsPosted += t.Amount
	cr.CreditsPosted += t.Amount
	t.CreatedAt = now()
	t.Pending = false
	cp := t
	c.transfers[t.ID] = &cp
	c.changedLocked()
	c.eventLocked(t)
	return Result{Code: OK}, nil
}

// PendingTransfer reserves amounts (two-phase; code 1 authorise / 6 hold).
func (c *DevClient) PendingTransfer(t Transfer) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.transfers[t.ID]; ok {
		if sameAttrs(*existing, t) && existing.Pending && !existing.Resolved {
			return Result{Code: Exists}, nil
		}
		return Result{Code: ExistsWithDifferentAttributes}, nil
	}
	dr, cr, res := c.validateParties(&t)
	if res.Code != OK {
		return res, nil
	}
	if rc := checkConstraints(dr, t.Amount, 0, 0, 0); rc != OK {
		return Result{Code: rc, Message: "debit account"}, nil
	}
	if rc := checkConstraints(cr, 0, 0, t.Amount, 0); rc != OK {
		return Result{Code: rc, Message: "credit account"}, nil
	}
	dr.DebitsPending += t.Amount
	cr.CreditsPending += t.Amount
	t.CreatedAt = now()
	t.Pending = true
	if t.TimeoutSeconds > 0 {
		t.ExpiresAt = time.Now().UTC().Add(time.Duration(t.TimeoutSeconds) * time.Second).Format(time.RFC3339Nano)
	}
	cp := t
	c.transfers[t.ID] = &cp
	c.changedLocked()
	c.eventLocked(t)
	return Result{Code: OK}, nil
}

// PostPending captures a pending transfer (code 2). Amount may be less than
// the pending amount; the pending transfer is fully resolved either way.
func (c *DevClient) PostPending(pendingID ID, amount uint64, code uint16) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pt, ok := c.transfers[pendingID]
	if !ok {
		return Result{Code: PendingTransferNotFound}, nil
	}
	if !pt.Pending || pt.Resolved {
		return Result{Code: PendingTransferNotPending}, nil
	}
	if amount == 0 {
		amount = pt.Amount
	}
	if amount > pt.Amount {
		return Result{Code: ExceedsPendingAmount}, nil
	}
	dr := c.accounts[pt.DebitAccountID]
	cr := c.accounts[pt.CreditAccountID]
	// release pending reservation, then post the (possibly reduced) amount
	dr.DebitsPending -= pt.Amount
	cr.CreditsPending -= pt.Amount
	if rc := checkConstraints(dr, 0, amount, 0, 0); rc != OK {
		dr.DebitsPending += pt.Amount
		cr.CreditsPending += pt.Amount
		return Result{Code: rc, Message: "debit account"}, nil
	}
	if rc := checkConstraints(cr, 0, 0, 0, amount); rc != OK {
		dr.DebitsPending += pt.Amount
		cr.CreditsPending += pt.Amount
		return Result{Code: rc, Message: "credit account"}, nil
	}
	dr.DebitsPosted += amount
	cr.CreditsPosted += amount
	pt.Resolved = true
	post := Transfer{
		ID:              pendingID, // post reuses the pending id (TB post_pending_transfer)
		DebitAccountID:  pt.DebitAccountID,
		CreditAccountID: pt.CreditAccountID,
		Amount:          amount,
		Ledger:          pt.Ledger,
		Code:            code,
		Pending:         false,
		PendingID:       pendingID,
		UserData:        pt.UserData,
		CreatedAt:       now(),
	}
	c.transfers[pendingID] = &post
	c.changedLocked()
	c.eventLocked(post)
	return Result{Code: OK}, nil
}

// VoidPending releases a pending reservation (code 3 / 7 release).
func (c *DevClient) VoidPending(pendingID ID, code uint16) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pt, ok := c.transfers[pendingID]
	if !ok {
		return Result{Code: PendingTransferNotFound}, nil
	}
	if !pt.Pending || pt.Resolved {
		return Result{Code: PendingTransferNotPending}, nil
	}
	dr := c.accounts[pt.DebitAccountID]
	cr := c.accounts[pt.CreditAccountID]
	dr.DebitsPending -= pt.Amount
	cr.CreditsPending -= pt.Amount
	pt.Resolved = true
	voided := *pt
	voided.Code = code
	voided.Pending = false
	voided.CreatedAt = now()
	c.transfers[pendingID] = &voided
	c.changedLocked()
	c.eventLocked(voided)
	return Result{Code: OK}, nil
}

// ExpirePendings voids every unresolved pending transfer whose ExpiresAt is
// at/before t (audit cross-cutting: stale pendings were permanent holds).
// Returns the voided transfers so the caller can emit expiry events.
func (c *DevClient) ExpirePendings(t time.Time) []Transfer {
	c.mu.Lock()
	defer c.mu.Unlock()
	var expired []Transfer
	for _, pt := range c.transfers {
		if !pt.Pending || pt.Resolved || pt.ExpiresAt == "" {
			continue
		}
		exp, err := time.Parse(time.RFC3339Nano, pt.ExpiresAt)
		if err != nil || exp.After(t) {
			continue
		}
		dr := c.accounts[pt.DebitAccountID]
		cr := c.accounts[pt.CreditAccountID]
		dr.DebitsPending -= pt.Amount
		cr.CreditsPending -= pt.Amount
		pt.Resolved = true
		voided := *pt
		voided.Code = CodeVoid
		voided.Pending = false
		voided.CreatedAt = now()
		c.transfers[pt.ID] = &voided
		expired = append(expired, voided)
	}
	if len(expired) > 0 {
		sort.Slice(expired, func(i, j int) bool { return expired[i].ID.String() < expired[j].ID.String() })
		c.changedLocked()
		for _, v := range expired {
			c.eventLocked(v)
		}
	}
	return expired
}

func sameAttrs(a, b Transfer) bool {
	return a.DebitAccountID == b.DebitAccountID &&
		a.CreditAccountID == b.CreditAccountID &&
		a.Amount == b.Amount && a.Ledger == b.Ledger && a.Code == b.Code
}

// Balance returns the derived balance view for an account.
func (c *DevClient) Balance(accountID ID) (Balance, Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.accounts[accountID]
	if !ok {
		return Balance{}, Result{Code: AccountNotFound}, nil
	}
	return Balance{
		AccountID:      accountID,
		DebitsPending:  a.DebitsPending,
		DebitsPosted:   a.DebitsPosted,
		CreditsPending: a.CreditsPending,
		CreditsPosted:  a.CreditsPosted,
		PostedNet:      int64(a.CreditsPosted) - int64(a.DebitsPosted),
		AvailableKobo:  int64(a.CreditsPosted) - int64(a.DebitsPosted) - int64(a.DebitsPending),
	}, Result{Code: OK}, nil
}

// GetAccount fetches one account.
func (c *DevClient) GetAccount(id ID) (Account, Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.accounts[id]
	if !ok {
		return Account{}, Result{Code: AccountNotFound}, nil
	}
	return *a, Result{Code: OK}, nil
}

// GetTransfer fetches one transfer.
func (c *DevClient) GetTransfer(id ID) (Transfer, Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.transfers[id]
	if !ok {
		return Transfer{}, Result{Code: PendingTransferNotFound}, nil
	}
	return *t, Result{Code: OK}, nil
}

// ListAccounts returns all accounts sorted by id.
func (c *DevClient) ListAccounts() ([]Account, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Account, 0, len(c.accounts))
	for _, a := range c.accounts {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out, nil
}

// ListTransfers returns transfers touching an account (either side).
func (c *DevClient) ListTransfers(accountID ID) ([]Transfer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Transfer
	for _, t := range c.transfers {
		if accountID.Zero() || t.DebitAccountID == accountID || t.CreditAccountID == accountID {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}
