package httpx

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"
)

// Regression (F-5, W4 HIGH): SIGTERM must trigger graceful
// http.Server.Shutdown (draining in-flight requests) instead of a hard kill,
// and the server must carry full timeouts, not only ReadHeaderTimeout.
func TestServeGracefulShutdownOnSIGTERM(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	started := make(chan struct{})
	srv := &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			time.Sleep(300 * time.Millisecond) // in-flight request during SIGTERM
			fmt.Fprint(w, "done")
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- Serve(srv) }()

	// wait for the listener, then start an in-flight request
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not start listening")
		}
		time.Sleep(10 * time.Millisecond)
	}
	respCh := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			respCh <- "err: " + err.Error()
			return
		}
		defer resp.Body.Close()
		buf := make([]byte, 16)
		n, _ := resp.Body.Read(buf)
		respCh <- string(buf[:n])
	}()
	<-started

	// SIGTERM self: Serve must drain the in-flight request and return nil.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Serve returned %v; want clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after SIGTERM")
	}
	select {
	case got := <-respCh:
		if got != "done" {
			t.Fatalf("in-flight request not drained: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request lost — hard kill, not graceful shutdown")
	}
}

// NewServer must set full timeouts (F-5 slowloris hardening).
func TestListenAndServeTimeoutDefaults(t *testing.T) {
	srv := NewServer("127.0.0.1:0", http.NewServeMux())
	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.WriteTimeout == 0 || srv.IdleTimeout == 0 {
		t.Fatalf("full timeouts required alongside ReadHeaderTimeout: %+v", srv)
	}
}
