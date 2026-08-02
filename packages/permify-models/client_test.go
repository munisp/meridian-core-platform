// client_test.go — live Permify client against a fake transport.
package permifymodels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "t1")
	c.logf = func(string, ...any) {} // silence circuit logs in tests
	return c
}

func TestClientCheckAllowed(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/t1/permissions/check" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var req checkRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Entity != (ref{Type: "tenant", ID: "core"}) ||
			req.Permission != PermTenantManage ||
			req.Subject != (ref{Type: "user", ID: "u1"}) {
			t.Errorf("bad check payload %+v", req)
		}
		w.Write([]byte(`{"can":"RESULT_ALLOWED"}`))
	})
	ok, err := c.Check(context.Background(), "tenant:core", PermTenantManage, "user:u1")
	if err != nil || !ok {
		t.Fatalf("want allowed, got ok=%v err=%v", ok, err)
	}
}

func TestClientCheckDenied(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"can":"RESULT_DENIED"}`))
	})
	ok, err := c.Check(context.Background(), "tenant:core", PermTenantManage, "user:u2")
	if err != nil || ok {
		t.Fatalf("want denied nil-error, got ok=%v err=%v", ok, err)
	}
}

func TestClientCheckRetriesOn5xx(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(`bad gateway`))
			return
		}
		w.Write([]byte(`{"can":"RESULT_ALLOWED"}`))
	})
	ok, err := c.Check(context.Background(), "tenant:core", PermTenantRead, "user:u1")
	if err != nil || !ok {
		t.Fatalf("want allowed after retry, got ok=%v err=%v", ok, err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("want exactly 2 attempts (one retry), got %d", got)
	}
}

func TestClientCheck5xxExhaustsRetry(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	ok, err := c.Check(context.Background(), "tenant:core", PermTenantRead, "user:u1")
	if err == nil || ok {
		t.Fatalf("want error+denied, got ok=%v err=%v", ok, err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("want 2 attempts, got %d", got)
	}
}

func TestClientCheckNoRetryOn4xx(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	})
	if _, err := c.Check(context.Background(), "tenant:core", PermTenantRead, "user:u1"); err == nil {
		t.Fatal("want error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("4xx must not retry, got %d attempts", got)
	}
}

func TestClientCheckTimeout(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte(`{"can":"RESULT_ALLOWED"}`))
	})
	c.timeout = 50 * time.Millisecond
	ok, err := c.Check(context.Background(), "tenant:core", PermTenantRead, "user:u1")
	if err == nil || ok {
		t.Fatalf("want timeout error+denied, got ok=%v err=%v", ok, err)
	}
	if !strings.Contains(err.Error(), "transport") {
		t.Fatalf("want transport timeout error, got %v", err)
	}
}

func TestNewClientFromEnv(t *testing.T) {
	t.Setenv("PERMIFY_URL", "")
	if NewClientFromEnv() != nil {
		t.Fatal("unset PERMIFY_URL must yield nil client (dev fallback)")
	}
	t.Setenv("PERMIFY_URL", "http://permify:3476/")
	t.Setenv("PERMIFY_TENANT", "meridian")
	c := NewClientFromEnv()
	if c == nil || c.base != "http://permify:3476" || c.tenant != "meridian" {
		t.Fatalf("bad env client %+v", c)
	}
}

func TestSplitRefValidation(t *testing.T) {
	c := NewClient("http://x", "")
	if _, err := c.Check(context.Background(), "tenant", "read", "user:u1"); err == nil {
		t.Fatal("malformed entity ref must error")
	}
}
