package main

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// FF-1 regression: concurrent GET /users (list encode), POST /login (read +
// marshal) and PUT /users/:id (update) must not data-race on shared *User
// pointers. Run with `go test -race`. Before the fix, handleUsers encoded
// shared *User values after unlock while handleUserUpdate mutated them in
// place, producing a DATA RACE warning.
func TestConcurrentUserReadWriteNoRace(t *testing.T) {
	a := &app{store: NewStore(), authMode: "dev"}

	// create a target user to race on
	body := `{"email":"race@meridian.local","name":"Race User","password":"pw-race-123"}`
	a.handleUserCreate(httptest.NewRecorder(),
		httptest.NewRequest("POST", "/v1/admin/users", strings.NewReader(body)))
	u := a.store.Users["race@meridian.local"]
	if u == nil {
		t.Fatal("setup: user not created")
	}
	uid := u.ID

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// writers: in-place user updates
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					req := httptest.NewRequest("PUT", "/v1/admin/users/"+uid,
						strings.NewReader(`{"name":"Race User","status":"active","roles":["admin","operator"]}`))
					req.SetPathValue("id", uid)
					a.handleUserUpdate(httptest.NewRecorder(), req)
				}
			}
		}()
	}

	// readers: list users (sorts + JSON-encodes)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					a.handleUsers(httptest.NewRecorder(),
						httptest.NewRequest("GET", "/v1/admin/users", nil))
				}
			}
		}()
	}

	// readers: login (reads Status/Roles and marshals the user)
	wg.Add(1)
	go func() {
		defer wg.Done()
		login := `{"email":"race@meridian.local","password":"pw-race-123"}`
		for {
			select {
			case <-stop:
				return
			default:
				a.handleLogin(httptest.NewRecorder(),
					httptest.NewRequest("POST", "/v1/admin/login", strings.NewReader(login)))
			}
		}
	}()

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// FF-1 regression (semantic): an update must be copy-on-write — a snapshot
// taken before the update must not observe the mutation.
func TestUserUpdateCopyOnWrite(t *testing.T) {
	a := &app{store: NewStore(), authMode: "dev"}
	a.handleUserCreate(httptest.NewRecorder(),
		httptest.NewRequest("POST", "/v1/admin/users",
			strings.NewReader(`{"email":"cow@meridian.local","name":"COW","password":"pw-cow-123"}`)))
	before := a.store.Users["cow@meridian.local"]
	if before == nil {
		t.Fatal("setup: user not created")
	}
	origName := before.Name

	req := httptest.NewRequest("PUT", "/v1/admin/users/"+before.ID,
		strings.NewReader(`{"name":"COW Updated"}`))
	req.SetPathValue("id", before.ID)
	a.handleUserUpdate(httptest.NewRecorder(), req)

	if before.Name != origName {
		t.Fatalf("previously returned *User was mutated in place: %q -> %q", origName, before.Name)
	}
	after := a.store.Users["cow@meridian.local"]
	if after == before {
		t.Fatal("update must replace the stored pointer, not mutate it")
	}
	if after.Name != "COW Updated" {
		t.Fatalf("update not applied: got %q", after.Name)
	}
}
