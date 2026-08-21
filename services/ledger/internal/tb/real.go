// Real TigerBeetle cluster client (HARDENING H3). Selected when
// TIGERBEETLE_ADDRESSES is set (comma-separated host:port list). Implements
// the same LedgerClient interface as DevClient with identical semantics:
// ledger ids 100-700, transfer codes 1-7 (1=authorise/pending,
// 2=capture/post_pending, 3=void, 4=topup, 5=settle, 6=hold, 7=release) and
// DEBITS_MUST_NOT_EXCEED_CREDITS enforced cluster-side.
package tb

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"strings"

	tbclient "github.com/tigerbeetle/tigerbeetle-go"
	tbtypes "github.com/tigerbeetle/tigerbeetle-go/pkg/types"
)

// RealClient is a TigerBeetle cluster-backed LedgerClient.
type RealClient struct {
	c tbclient.Client
}

// NewRealClient connects to a TigerBeetle cluster (cluster id 0).
func NewRealClient(addresses []string) (*RealClient, error) {
	if len(addresses) == 0 {
		return nil, fmt.Errorf("tigerbeetle: at least one address required")
	}
	c, err := tbclient.NewClient(tbtypes.ToUint128(0), addresses)
	if err != nil {
		return nil, fmt.Errorf("tigerbeetle: connect %v: %w", addresses, err)
	}
	return &RealClient{c: c}, nil
}

// NewRealClientFromEnv builds the client from TIGERBEETLE_ADDRESSES.
func NewRealClientFromEnv() (*RealClient, error) {
	var addrs []string
	for _, a := range strings.Split(os.Getenv("TIGERBEETLE_ADDRESSES"), ",") {
		if s := strings.TrimSpace(a); s != "" {
			addrs = append(addrs, s)
		}
	}
	return NewRealClient(addrs)
}

// Close releases the cluster connection.
func (c *RealClient) Close() { c.c.Close() }

// RandomSerial allocates a collision-resistant 63-bit serial for account
// creation in prod (the cluster is the system of record; dev uses the
// DevClient monotonic allocator).
func RandomSerial() uint64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint64(b[:]) >> 1
}

func idToU128(id ID) tbtypes.Uint128 {
	u, err := tbtypes.HexStringToUint128(id.String())
	if err != nil { // cannot happen: ID.String is 32 lowercase hex chars
		return tbtypes.Uint128{}
	}
	return u
}

func u128ToID(u tbtypes.Uint128) ID {
	b := u.Bytes() // little-endian
	for i, j := 0, 15; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	var id ID
	id.High = binary.BigEndian.Uint64(b[0:8])
	id.Low = binary.BigEndian.Uint64(b[8:16])
	return id
}

func u128ToU64(u tbtypes.Uint128) uint64 {
	bi := u.BigInt()
	return bi.Uint64()
}

func accountToTB(a Account) tbtypes.Account {
	return tbtypes.Account{
		ID:     idToU128(a.ID),
		Ledger: uint32(a.Ledger),
		Code:   a.Code,
		Flags: tbtypes.AccountFlags{
			Linked:                     a.Flags&FlagLinked != 0,
			DebitsMustNotExceedCredits: a.Flags&FlagDebitsMustNotExceedCredits != 0,
			CreditsMustNotExceedDebits: a.Flags&FlagCreditsMustNotExceedDebits != 0,
			History:                    true, // enable GetAccountTransfers/History
		}.ToUint16(),
	}
}

func accountFromTB(a tbtypes.Account) Account {
	var flags uint16
	tf := a.AccountFlags()
	if tf.Linked {
		flags |= FlagLinked
	}
	if tf.DebitsMustNotExceedCredits {
		flags |= FlagDebitsMustNotExceedCredits
	}
	if tf.CreditsMustNotExceedDebits {
		flags |= FlagCreditsMustNotExceedDebits
	}
	return Account{
		ID:             u128ToID(a.ID),
		Ledger:         uint64(a.Ledger),
		Code:           a.Code,
		Flags:          flags,
		DebitsPending:  u128ToU64(a.DebitsPending),
		DebitsPosted:   u128ToU64(a.DebitsPosted),
		CreditsPending: u128ToU64(a.CreditsPending),
		CreditsPosted:  u128ToU64(a.CreditsPosted),
	}
}

func transferToTB(t Transfer, pending bool) tbtypes.Transfer {
	return tbtypes.Transfer{
		ID:              idToU128(t.ID),
		DebitAccountID:  idToU128(t.DebitAccountID),
		CreditAccountID: idToU128(t.CreditAccountID),
		Amount:          tbtypes.ToUint128(t.Amount),
		PendingID:       idToU128(t.PendingID),
		Ledger:          uint32(t.Ledger),
		Code:            t.Code,
		Flags: tbtypes.TransferFlags{
			Pending:             pending,
			PostPendingTransfer: t.Code == CodeCapture,
			VoidPendingTransfer: t.Code == CodeVoid && !t.PendingID.Zero(),
		}.ToUint16(),
	}
}

func transferFromTB(t tbtypes.Transfer) Transfer {
	f := t.TransferFlags()
	return Transfer{
		ID:              u128ToID(t.ID),
		DebitAccountID:  u128ToID(t.DebitAccountID),
		CreditAccountID: u128ToID(t.CreditAccountID),
		Amount:          u128ToU64(t.Amount),
		Ledger:          uint64(t.Ledger),
		Code:            t.Code,
		Pending:         f.Pending,
		PendingID:       u128ToID(t.PendingID),
		Resolved:        f.PostPendingTransfer || f.VoidPendingTransfer,
	}
}

func accountResultCode(r tbtypes.CreateAccountResult) ResultCode {
	switch r {
	case tbtypes.AccountOK:
		return OK
	case tbtypes.AccountExists:
		return Exists
	case tbtypes.AccountExistsWithDifferentFlags,
		tbtypes.AccountExistsWithDifferentLedger,
		tbtypes.AccountExistsWithDifferentCode,
		tbtypes.AccountExistsWithDifferentUserData128,
		tbtypes.AccountExistsWithDifferentUserData64,
		tbtypes.AccountExistsWithDifferentUserData32:
		return ExistsWithDifferentAttributes
	default:
		return ResultCode("account_" + strings.ToLower(strings.TrimPrefix(r.String(), "Account")))
	}
}

func transferResultCode(r tbtypes.CreateTransferResult) ResultCode {
	switch r {
	case tbtypes.TransferOK:
		return OK
	case tbtypes.TransferExists:
		return Exists
	case tbtypes.TransferExistsWithDifferentFlags,
		tbtypes.TransferExistsWithDifferentAmount,
		tbtypes.TransferExistsWithDifferentDebitAccountID,
		tbtypes.TransferExistsWithDifferentCreditAccountID,
		tbtypes.TransferExistsWithDifferentPendingID:
		return ExistsWithDifferentAttributes
	case tbtypes.TransferDebitAccountNotFound, tbtypes.TransferCreditAccountNotFound:
		return AccountNotFound
	case tbtypes.TransferAccountsMustBeDifferent:
		return AccountsMustBeDifferent
	case tbtypes.TransferAmountMustNotBeZero:
		return AmountMustBePositive
	case tbtypes.TransferExceedsCredits:
		return ExceedsCredits
	case tbtypes.TransferExceedsDebits:
		return ExceedsDebits
	case tbtypes.TransferAccountsMustHaveTheSameLedger, tbtypes.TransferTransferMustHaveTheSameLedgerAsAccounts:
		return LedgerMustMatch
	case tbtypes.TransferPendingTransferNotFound:
		return PendingTransferNotFound
	case tbtypes.TransferPendingTransferNotPending:
		return PendingTransferNotPending
	case tbtypes.TransferExceedsPendingTransferAmount:
		return ExceedsPendingAmount
	case tbtypes.TransferPendingTransferHasDifferentDebitAccountID,
		tbtypes.TransferPendingTransferHasDifferentCreditAccountID,
		tbtypes.TransferPendingTransferHasDifferentLedger,
		tbtypes.TransferPendingTransferHasDifferentCode,
		tbtypes.TransferPendingTransferHasDifferentAmount:
		return PendingTransferHasDifferentAttr
	case tbtypes.TransferPendingTransferAlreadyPosted, tbtypes.TransferPendingTransferAlreadyVoided:
		return TransferNotPending
	case tbtypes.TransferOverflowsDebits, tbtypes.TransferOverflowsCredits,
		tbtypes.TransferOverflowsDebitsPending, tbtypes.TransferOverflowsCreditsPending,
		tbtypes.TransferOverflowsDebitsPosted, tbtypes.TransferOverflowsCreditsPosted:
		return Overflows
	default:
		return ResultCode("transfer_" + strings.ToLower(strings.TrimPrefix(r.String(), "Transfer")))
	}
}

// CreateAccounts creates accounts on the cluster.
func (c *RealClient) CreateAccounts(accts []Account) ([]Result, error) {
	tba := make([]tbtypes.Account, len(accts))
	for i, a := range accts {
		tba[i] = accountToTB(a)
	}
	res, err := c.c.CreateAccounts(tba)
	if err != nil {
		return nil, err
	}
	out := make([]Result, len(accts))
	for i := range out {
		out[i] = Result{Code: OK}
	}
	for _, r := range res {
		out[r.Index] = Result{Code: accountResultCode(r.Result), Message: r.Result.String()}
	}
	return out, nil
}

// Transfer executes an immediate double-entry transfer (codes 4/5 etc).
func (c *RealClient) Transfer(t Transfer) (Result, error) {
	return c.createOne(transferToTB(t, false))
}

// PendingTransfer executes an authorise/hold (codes 1/6, pending flag).
func (c *RealClient) PendingTransfer(t Transfer) (Result, error) {
	return c.createOne(transferToTB(t, true))
}

// PostPending captures a pending transfer (post_pending_transfer).
// TigerBeetle requires the post to carry the SAME code as the pending
// transfer (PENDING_TRANSFER_HAS_DIFFERENT_CODE otherwise): code=0 reuses
// the pending transfer's code; a non-zero code must match it. The previous
// behaviour (defaulting to CodeCapture=2 while authorise uses code 1) was
// rejected by a real cluster and only passed against the DevClient.
func (c *RealClient) PostPending(pendingID ID, amount uint64, code uint16) (Result, error) {
	pend, err := c.lookupTransfer(pendingID)
	if err != nil {
		return Result{Code: PendingTransferNotFound}, nil
	}
	if code != 0 && code != pend.Code {
		return Result{Code: PendingTransferHasDifferentAttr, Message: "code must match the pending transfer's code"}, nil
	}
	t := tbtypes.Transfer{
		ID:              idToU128(pendingResolutionID(pendingID, pend.Code)),
		DebitAccountID:  pend.DebitAccountID,
		CreditAccountID: pend.CreditAccountID,
		Amount:          tbtypes.ToUint128(amount),
		PendingID:       idToU128(pendingID),
		Ledger:          pend.Ledger,
		Code:            pend.Code,
		Flags:           tbtypes.TransferFlags{PostPendingTransfer: true}.ToUint16(),
	}
	return c.createOne(t)
}

// PostPendingAs captures a pending transfer like PostPending, but records
// the post under the caller-supplied postID (FF-3) instead of deriving the
// resolution id, so callers that durably persisted the post id can resolve
// it afterwards. The code-reuse rule is identical to PostPending; a replay
// with the same postID is idempotent at the TigerBeetle level (the cluster
// returns EXISTS for an identical transfer, which we surface as OK).
func (c *RealClient) PostPendingAs(pendingID, postID ID, amount uint64, code uint16) (Result, error) {
	pend, err := c.lookupTransfer(pendingID)
	if err != nil {
		return Result{Code: PendingTransferNotFound}, nil
	}
	if code != 0 && code != pend.Code {
		return Result{Code: PendingTransferHasDifferentAttr, Message: "code must match the pending transfer's code"}, nil
	}
	if amount == 0 {
		pendAmt := pend.Amount.BigInt()
		amount = pendAmt.Uint64()
	}
	t := tbtypes.Transfer{
		ID:              idToU128(postID),
		DebitAccountID:  pend.DebitAccountID,
		CreditAccountID: pend.CreditAccountID,
		Amount:          tbtypes.ToUint128(amount),
		PendingID:       idToU128(pendingID),
		Ledger:          pend.Ledger,
		Code:            pend.Code,
		Flags:           tbtypes.TransferFlags{PostPendingTransfer: true}.ToUint16(),
	}
	res, err := c.createOne(t)
	if err != nil {
		return res, err
	}
	if res.Code == Exists {
		// Idempotent replay of the same post (same id + same attributes).
		if prev, lerr := c.lookupTransfer(postID); lerr == nil &&
			prev.PendingID == idToU128(pendingID) &&
			prev.Code == pend.Code &&
			func() bool { a := prev.Amount.BigInt(); return a.Uint64() == amount }() {
			return Result{Code: OK}, nil
		}
		return Result{Code: ExistsWithDifferentAttributes, Message: "post id already in use"}, nil
	}
	return res, nil
}

// VoidPending voids/releases a pending transfer (void_pending_transfer).
// The void must carry the pending transfer's code, exactly as PostPending.
func (c *RealClient) VoidPending(pendingID ID, code uint16) (Result, error) {
	pend, err := c.lookupTransfer(pendingID)
	if err != nil {
		return Result{Code: PendingTransferNotFound}, nil
	}
	if code != 0 && code != pend.Code {
		return Result{Code: PendingTransferHasDifferentAttr, Message: "code must match the pending transfer's code"}, nil
	}
	t := tbtypes.Transfer{
		ID:              idToU128(pendingResolutionID(pendingID, pend.Code)),
		DebitAccountID:  pend.DebitAccountID,
		CreditAccountID: pend.CreditAccountID,
		PendingID:       idToU128(pendingID),
		Ledger:          pend.Ledger,
		Code:            pend.Code,
		Flags:           tbtypes.TransferFlags{VoidPendingTransfer: true}.ToUint16(),
	}
	return c.createOne(t)
}

// pendingResolutionID derives the post/void transfer id from the pending id
// (low serial adjusted by code) — mirrors DevClient's deterministic ids.
func pendingResolutionID(pendingID ID, code uint16) ID {
	return ID{High: pendingID.High, Low: pendingID.Low + uint64(code)<<48}
}

func (c *RealClient) createOne(t tbtypes.Transfer) (Result, error) {
	res, err := c.c.CreateTransfers([]tbtypes.Transfer{t})
	if err != nil {
		return Result{}, err
	}
	if len(res) == 0 {
		return Result{Code: OK}, nil
	}
	return Result{Code: transferResultCode(res[0].Result), Message: res[0].Result.String()}, nil
}

func (c *RealClient) lookupTransfer(id ID) (tbtypes.Transfer, error) {
	ts, err := c.c.LookupTransfers([]tbtypes.Uint128{idToU128(id)})
	if err != nil {
		return tbtypes.Transfer{}, err
	}
	if len(ts) == 0 {
		return tbtypes.Transfer{}, fmt.Errorf("transfer not found")
	}
	return ts[0], nil
}

// Balance returns the derived balance view for an account.
func (c *RealClient) Balance(accountID ID) (Balance, Result, error) {
	acct, res, err := c.GetAccount(accountID)
	if err != nil || res.Code != OK {
		return Balance{}, res, err
	}
	return Balance{
		AccountID:      accountID,
		DebitsPending:  acct.DebitsPending,
		DebitsPosted:   acct.DebitsPosted,
		CreditsPending: acct.CreditsPending,
		CreditsPosted:  acct.CreditsPosted,
		PostedNet:      int64(acct.CreditsPosted) - int64(acct.DebitsPosted),
		AvailableKobo:  int64(acct.CreditsPosted) - int64(acct.DebitsPosted) - int64(acct.DebitsPending),
	}, Result{Code: OK}, nil
}

// GetAccount fetches one account.
func (c *RealClient) GetAccount(id ID) (Account, Result, error) {
	as, err := c.c.LookupAccounts([]tbtypes.Uint128{idToU128(id)})
	if err != nil {
		return Account{}, Result{}, err
	}
	if len(as) == 0 {
		return Account{}, Result{Code: AccountNotFound}, nil
	}
	return accountFromTB(as[0]), Result{Code: OK}, nil
}

// GetTransfer fetches one transfer.
func (c *RealClient) GetTransfer(id ID) (Transfer, Result, error) {
	t, err := c.lookupTransfer(id)
	if err != nil {
		return Transfer{}, Result{Code: PendingTransferNotFound}, nil
	}
	return transferFromTB(t), Result{Code: OK}, nil
}

const queryLimit = 8192

// ListAccounts returns up to 8192 accounts (query filter, no field filter).
func (c *RealClient) ListAccounts() ([]Account, error) {
	as, err := c.c.QueryAccounts(tbtypes.QueryFilter{Limit: queryLimit})
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(as))
	for _, a := range as {
		out = append(out, accountFromTB(a))
	}
	return out, nil
}

// ListTransfers returns debits+credits for an account (history flag enabled
// at account creation), oldest first.
func (c *RealClient) ListTransfers(accountID ID) ([]Transfer, error) {
	ts, err := c.c.GetAccountTransfers(tbtypes.AccountFilter{
		AccountID: idToU128(accountID),
		Limit:     queryLimit,
		Flags: tbtypes.AccountFilterFlags{
			Debits:  true,
			Credits: true,
		}.ToUint32(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Transfer, 0, len(ts))
	for _, t := range ts {
		out = append(out, transferFromTB(t))
	}
	return out, nil
}
