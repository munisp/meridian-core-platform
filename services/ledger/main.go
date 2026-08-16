// ledger — TigerBeetle ledger service (SPEC 1.5, 2).
// Dev mode runs the embedded DevClient (durable snapshot in DATA_DIR);
// TIGERBEETLE_ADDRESSES selects the real cluster client (see tb package).
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/bus"
	"github.com/munisp/meridian-core-platform/packages/events/envelope"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/packages/events/outbox"
	sdkx "github.com/munisp/meridian-core-platform/packages/temporal-sdkx"
	"github.com/munisp/meridian-core-platform/services/ledger/internal/tb"
)

const (
	service = "ledger"
	version = "0.1.0"
)

// devClientProfileOK enforces F-7: the in-mem DevClient may boot only with an
// explicit PROFILE=dev; PROFILE=prod without TIGERBEETLE_ADDRESSES is a boot
// error, and so is an unset/other profile (no implicit dev in-memory ledger).
func devClientProfileOK(profile string) error {
	switch profile {
	case "dev":
		return nil
	case "prod":
		return fmt.Errorf("profile=prod requires TIGERBEETLE_ADDRESSES; refusing to boot the in-mem DevClient")
	default:
		return fmt.Errorf("TIGERBEETLE_ADDRESSES unset and PROFILE=%q; the in-mem DevClient requires explicit PROFILE=dev", profile)
	}
}

// snapshotFile is the durable dev snapshot (atomic rewrite on change).
type snapshot struct {
	Accounts  []tb.Account      `json:"accounts"`
	Transfers []tb.Transfer     `json:"transfers"`
	Serials   map[uint64]uint64 `json:"serials"`
}

type server struct {
	client tb.LedgerClient
	dev    *tb.DevClient // non-nil only in dev profile (snapshots/hooks)
	out    outbox.Store
	dir    string
	thresh *thresholdTracker // I7: CTR ₦10m / structuring detection
	// wfRunner executes the money sagas (CaptureSaga/RefundWorkflow);
	// *sdkx.TemporalRunner when TEMPORAL_URL is set, inproc dev runner
	// otherwise (docs/temporal-migration.md).
	wfRunner sdkx.Runner
}

func main() {
	dir := httpx.Env("DATA_DIR", "./data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	// HARDENING H1: TIGERBEETLE_ADDRESSES set -> real cluster (profile=prod);
	// otherwise the durable in-mem DevClient (profile=dev).
	var client tb.LedgerClient
	var dev *tb.DevClient
	if os.Getenv("TIGERBEETLE_ADDRESSES") != "" {
		rc, err := tb.NewRealClientFromEnv()
		if err != nil {
			// FAIL CLOSED (audit: prod selector set but a connect failure
			// silently downgraded to the in-mem DevClient — the financial
			// system of record would run on ephemeral dev state). Refuse
			// to start instead.
			log.Fatalf("profile=prod component=ledger FATAL: TIGERBEETLE_ADDRESSES set but connect failed (%v); refusing to fall back to the in-mem DevClient", err)
		}
		log.Printf("profile=prod component=ledger tigerbeetle addresses=%s", os.Getenv("TIGERBEETLE_ADDRESSES"))
		client = rc
		defer rc.Close()
	} else {
		// FAIL CLOSED (F-7): the in-mem DevClient is the financial system of
		// record's dev stand-in — booting it must be an explicit choice. An
		// env omission in a prod deploy must never run the ledger on
		// ephemeral in-memory state.
		if err := devClientProfileOK(os.Getenv("PROFILE")); err != nil {
			log.Fatalf("component=ledger FATAL: %v", err)
		}
		log.Printf("profile=dev component=ledger in-mem")
		dev = tb.NewDevClient()
		client = dev
	}
	srv := &server{client: client, dev: dev, dir: dir, thresh: newThresholdTracker()}
	srv.initMoneyWorkflows()

	if dev != nil {
		// durable snapshot restore
		if b, err := os.ReadFile(filepath.Join(dir, "ledger.snapshot.json")); err == nil {
			var snap snapshot
			if json.Unmarshal(b, &snap) == nil {
				dev.Restore(snap.Accounts, snap.Transfers, snap.Serials)
				log.Printf("restored %d accounts, %d transfers", len(snap.Accounts), len(snap.Transfers))
			}
		}
		dev.SetHooks(srv.persist, srv.emitTransferEvent)
	}

	// outbox + relay
	ob, err := outbox.NewFileStore(filepath.Join(dir, "outbox"))
	if err != nil {
		log.Fatal(err)
	}
	defer ob.Close()
	srv.out = ob
	b := bus.NewFromEnv()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relay := outbox.Relay{Store: ob, Bus: b, Dir: filepath.Join(dir, "outbox")}
	go relay.Run(ctx)

	// F7: pending-expiry sweeper — voids unresolved pending transfers past
	// their timeout and emits nrs.ledger.pending_expired.v1 per expiry.
	// (Prod: TigerBeetle enforces pending timeouts natively on the cluster;
	// this sweeper covers the dev DevClient.)
	if dev != nil {
		go srv.runPendingSweeper(ctx)
	}

	httpx.InitMetrics(service, version)
	httpx.StartMetricsServer()
	handler := auth.Middleware(httpx.Instrument(srv.routes()))
	addr := ":" + httpx.Port("8010")
	log.Printf("%s %s (DATA_DIR=%s, EVENT_BUS=%s)", service, version, dir, httpx.Env("EVENT_BUS", "inproc"))
	log.Fatal(httpx.ListenAndServe(addr, handler))
}

// routes registers the HTTP API. Security (audit H-4): money-movement
// endpoints require explicit roles from the token claims — "ledger:admin"
// for account creation and "ledger:post" for transfer create/post/void.
// Authentication alone (any role, e.g. read-only auditor) is NOT enough:
// RequireRole answers 403 (RFC7807) for under-privileged callers.
func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	httpx.RegisterStandard(mux, service, version, nil)
	mux.HandleFunc("POST /v1/accounts", auth.RequireRole("ledger:admin", s.createAccounts))
	mux.HandleFunc("GET /v1/accounts", s.listAccounts)
	mux.HandleFunc("GET /v1/accounts/{id}/balance", s.getBalance)
	mux.HandleFunc("POST /v1/transfers", auth.RequireRole("ledger:post", s.createTransfer))
	mux.HandleFunc("POST /v1/transfers/pending", auth.RequireRole("ledger:post", s.createPending))
	mux.HandleFunc("POST /v1/transfers/{id}/post", auth.RequireRole("ledger:post", s.postPending))
	mux.HandleFunc("POST /v1/transfers/{id}/void", auth.RequireRole("ledger:post", s.voidPending))
	mux.HandleFunc("GET /v1/transfers", s.listTransfers)
	return mux
}

// runPendingSweeper voids expired pending transfers on an interval
// runPendingSweeper voids expired pending transfers on an interval
// (LEDGER_SWEEP_INTERVAL_SECONDS, default 30s) and emits
// nrs.ledger.pending_expired.v1 for each. Boot-time pass included so
// pendings that expired while the service was down are released.
func (s *server) runPendingSweeper(ctx context.Context) {
	s.sweepExpiredPendings()
	secs, _ := strconv.Atoi(httpx.Env("LEDGER_SWEEP_INTERVAL_SECONDS", "30"))
	if secs <= 0 {
		secs = 30
	}
	interval := time.Duration(secs) * time.Second
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.sweepExpiredPendings()
		}
	}
}

func (s *server) sweepExpiredPendings() {
	expired := s.dev.ExpirePendings(time.Now().UTC())
	for _, t := range expired {
		env, err := envelope.New("nrs.ledger.pending_expired.v1", service, "", "", map[string]any{
			"transfer_id":       t.ID.String(),
			"debit_account_id":  t.DebitAccountID.String(),
			"credit_account_id": t.CreditAccountID.String(),
			"amount_kobo":       t.Amount,
			"ledger":            t.Ledger,
			"expired_at":        t.ExpiresAt,
		})
		if err != nil {
			log.Printf("pending-expiry envelope: %v", err)
			continue
		}
		if err := s.out.Append("nrs.ledger.pending_expired.v1", env); err != nil {
			log.Printf("pending-expiry outbox append: %v", err)
		}
	}
	if len(expired) > 0 {
		log.Printf("pending-expiry sweeper: voided %d expired pending transfers", len(expired))
	}
}

func (s *server) persist() {
	if s.dev == nil {
		return // prod: TigerBeetle cluster is the system of record
	}
	accts, trs, sers := s.dev.Snapshot()
	b, err := json.Marshal(snapshot{Accounts: accts, Transfers: trs, Serials: sers})
	if err != nil {
		log.Printf("snapshot marshal: %v", err)
		return
	}
	tmp := filepath.Join(s.dir, "ledger.snapshot.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		log.Printf("snapshot write: %v", err)
		return
	}
	os.Rename(tmp, filepath.Join(s.dir, "ledger.snapshot.json"))
}

func (s *server) emitTransferEvent(t tb.Transfer) {
	env, err := envelope.New("nrs.ledger.transfers.v1", service, "", "", map[string]any{
		"transfer_id":       t.ID.String(),
		"debit_account_id":  t.DebitAccountID.String(),
		"credit_account_id": t.CreditAccountID.String(),
		"amount_kobo":       t.Amount,
		"ledger":            t.Ledger,
		"code":              t.Code,
		"pending":           t.Pending,
	})
	if err != nil {
		log.Printf("envelope: %v", err)
		return
	}
	if err := s.out.Append("nrs.ledger.transfers.v1", env); err != nil {
		log.Printf("outbox append: %v", err)
	}
	// I7: CTR ₦10m threshold + structuring-suspicion hooks (rp-bank-thresholds).
	s.checkThresholds(t)
}

// checkThresholds applies the rp-bank-thresholds rules to every transfer
// and emits nrs.aml.* events through the outbox.
func (s *server) checkThresholds(t tb.Transfer) {
	if s.thresh == nil {
		return
	}
	for _, ev := range s.thresh.Observe(t) {
		env, err := envelope.New(ev.Type, service, "", "", ev.Payload)
		if err != nil {
			log.Printf("threshold envelope: %v", err)
			continue
		}
		if err := s.out.Append(ev.Type, env); err != nil {
			log.Printf("threshold outbox append: %v", err)
		}
	}
}

type createAccountReq struct {
	Namespace uint64 `json:"namespace"`        // ledger id 100..700
	Serial    uint64 `json:"serial,omitempty"` // 0 => auto-allocate
	ID        string `json:"id,omitempty"`     // explicit 128-bit hex (advanced)
	Flags     uint16 `json:"flags"`
	UserData  string `json:"user_data,omitempty"`
	Code      uint16 `json:"code,omitempty"`
}

func (s *server) createAccounts(w http.ResponseWriter, r *http.Request) {
	var req createAccountReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	var id tb.ID
	if req.ID != "" {
		var err error
		id, err = tb.ParseID(req.ID)
		if err != nil {
			httpx.BadRequest(w, "%v", err)
			return
		}
	} else {
		serial := req.Serial
		if serial == 0 {
			if s.dev != nil {
				serial = s.dev.NextSerial(req.Namespace)
			} else {
				serial = tb.RandomSerial()
			}
		}
		id = tb.MakeID(req.Namespace, serial)
	}
	code := req.Code
	if code == 0 {
		code = 1
	}
	res, err := s.client.CreateAccounts([]tb.Account{{
		ID: id, Ledger: req.Namespace, Code: code, Flags: req.Flags, UserData: req.UserData,
	}})
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	status := http.StatusCreated
	if res[0].Code != tb.OK && res[0].Code != tb.Exists {
		status = http.StatusUnprocessableEntity
	} else if res[0].Code == tb.Exists {
		status = http.StatusOK
	}
	acct, _, _ := s.client.GetAccount(id)
	httpx.JSON(w, status, map[string]any{"result": res[0], "account": acct})
}

func (s *server) listAccounts(w http.ResponseWriter, r *http.Request) {
	accts, err := s.client.ListAccounts()
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"accounts": accts})
}

func (s *server) getBalance(w http.ResponseWriter, r *http.Request) {
	id, err := tb.ParseID(r.PathValue("id"))
	if err != nil {
		httpx.BadRequest(w, "%v", err)
		return
	}
	bal, res, err := s.client.Balance(id)
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	if res.Code != tb.OK {
		httpx.NotFound(w, "account %s", id.String())
		return
	}
	httpx.JSON(w, http.StatusOK, bal)
}

type transferReq struct {
	ID              string `json:"id,omitempty"`
	DebitAccountID  string `json:"debit_account_id"`
	CreditAccountID string `json:"credit_account_id"`
	AmountKobo      uint64 `json:"amount_kobo"`
	Ledger          uint64 `json:"ledger"`
	Code            uint16 `json:"code"`
	UserData        string `json:"user_data,omitempty"`
	TimeoutSeconds  uint32 `json:"timeout_seconds,omitempty"` // pending expiry (0 = no expiry)
}

func randomID() tb.ID {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	var id tb.ID
	for i := 0; i < 8; i++ {
		id.High = id.High<<8 | uint64(b[i])
		id.Low = id.Low<<8 | uint64(b[8+i])
	}
	return id
}

func (s *server) buildTransfer(req transferReq) (tb.Transfer, error) {
	var t tb.Transfer
	var err error
	if req.ID != "" {
		t.ID, err = tb.ParseID(req.ID)
		if err != nil {
			return t, err
		}
	} else {
		t.ID = randomID()
	}
	if t.DebitAccountID, err = tb.ParseID(req.DebitAccountID); err != nil {
		return t, err
	}
	if t.CreditAccountID, err = tb.ParseID(req.CreditAccountID); err != nil {
		return t, err
	}
	t.Amount = req.AmountKobo
	t.Ledger = req.Ledger
	t.Code = req.Code
	t.UserData = req.UserData
	t.TimeoutSeconds = req.TimeoutSeconds
	return t, nil
}

func writeTransferResult(w http.ResponseWriter, id tb.ID, res tb.Result) {
	status := http.StatusCreated
	switch res.Code {
	case tb.OK:
	case tb.Exists:
		status = http.StatusOK
	case tb.AccountNotFound:
		status = http.StatusNotFound
	default:
		status = http.StatusUnprocessableEntity
	}
	httpx.JSON(w, status, map[string]any{"result": res, "transfer_id": id.String()})
}

func (s *server) createTransfer(w http.ResponseWriter, r *http.Request) {
	var req transferReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	t, err := s.buildTransfer(req)
	if err != nil {
		httpx.BadRequest(w, "%v", err)
		return
	}
	res, err := s.client.Transfer(t)
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	s.emitProd(res, t)
	writeTransferResult(w, t.ID, res)
}

// emitProd emits the outbox transfer event in prod profile (in dev the
// DevClient onChange/onEvent hooks do it).
func (s *server) emitProd(res tb.Result, t tb.Transfer) {
	if s.dev == nil && res.Code == tb.OK {
		s.emitTransferEvent(t)
	}
}

func (s *server) createPending(w http.ResponseWriter, r *http.Request) {
	var req transferReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	if req.Code == 0 {
		req.Code = tb.CodeAuthorise
	}
	t, err := s.buildTransfer(req)
	if err != nil {
		httpx.BadRequest(w, "%v", err)
		return
	}
	res, err := s.client.PendingTransfer(t)
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	s.emitProd(res, t)
	writeTransferResult(w, t.ID, res)
}

type postReq struct {
	AmountKobo uint64 `json:"amount_kobo,omitempty"` // 0 => full pending amount
	Code       uint16 `json:"code,omitempty"`
}

func (s *server) postPending(w http.ResponseWriter, r *http.Request) {
	id, err := tb.ParseID(r.PathValue("id"))
	if err != nil {
		httpx.BadRequest(w, "%v", err)
		return
	}
	var req postReq
	_ = httpx.Decode(r, &req) // empty body allowed
	// TigerBeetle requires a post/void to reuse the pending transfer's own
	// code; req.Code stays 0 unless the caller asserts a specific code, and
	// the ledger client rejects a mismatch (PENDING_TRANSFER_HAS_DIFFERENT_CODE).
	res, err := s.client.PostPending(id, req.AmountKobo, req.Code)
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	writeTransferResult(w, id, res)
}

func (s *server) voidPending(w http.ResponseWriter, r *http.Request) {
	id, err := tb.ParseID(r.PathValue("id"))
	if err != nil {
		httpx.BadRequest(w, "%v", err)
		return
	}
	// code=0: the client reuses the pending transfer's code (TB rule).
	res, err := s.client.VoidPending(id, 0)
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	writeTransferResult(w, id, res)
}

func (s *server) listTransfers(w http.ResponseWriter, r *http.Request) {
	var id tb.ID
	if q := r.URL.Query().Get("account"); q != "" {
		var err error
		id, err = tb.ParseID(strings.TrimSpace(q))
		if err != nil {
			httpx.BadRequest(w, "%v", err)
			return
		}
	}
	trs, err := s.client.ListTransfers(id)
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"transfers": trs})
}
