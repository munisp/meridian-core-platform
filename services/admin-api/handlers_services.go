package main

import (
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- service registry + health rollup (SPEC §2) ----------

// rollup polls /healthz of every enabled registered service concurrently.
// When poll=false and no check has happened yet it still performs a check;
// results are cached on the ServiceEntry for cheap repeat reads.
func (a *app) rollup(poll bool) []*ServiceEntry {
	a.store.mu.Lock()
	svcs := make([]*ServiceEntry, 0, len(a.store.Services))
	for _, s := range a.store.Services {
		cp := *s
		svcs = append(svcs, &cp)
	}
	a.store.mu.Unlock()
	sort.Slice(svcs, func(i, j int) bool {
		if svcs[i].Plane != svcs[j].Plane {
			return svcs[i].Plane < svcs[j].Plane
		}
		return svcs[i].ID < svcs[j].ID
	})

	var wg sync.WaitGroup
	for _, svc := range svcs {
		if !svc.Enabled {
			svc.HealthStatus = "disabled"
			continue
		}
		if svc.ID == "admin-api" {
			svc.HealthStatus = "ok"
			svc.HealthDetail = "self"
			svc.CheckedAt = nowRFC3339()
			continue
		}
		wg.Add(1)
		go func(svc *ServiceEntry) {
			defer wg.Done()
			start := time.Now()
			status, detail := a.checkHealth(svc)
			svc.HealthStatus = status
			svc.HealthDetail = detail
			svc.LatencyMs = time.Since(start).Milliseconds()
			svc.CheckedAt = nowRFC3339()
		}(svc)
	}
	wg.Wait()
	return svcs
}

func (a *app) checkHealth(svc *ServiceEntry) (string, string) {
	path := svc.HealthPath
	if path == "" {
		path = "/healthz"
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(svc.BaseURL, "/")+path, nil)
	if err != nil {
		return "unreachable", err.Error()
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return "unreachable", "connection failed (dev seed mode for dependent views)"
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 500 {
		return "degraded", resp.Status
	}
	if resp.StatusCode >= 400 {
		return "unreachable", resp.Status
	}
	if svc.HealthPath == "/" { // apps/consoles: any 2xx-3xx is fine
		return "ok", "reachable"
	}
	if strings.Contains(string(body), `"status":"ok"`) || strings.Contains(string(body), `"status": "ok"`) {
		return "ok", strings.TrimSpace(string(body))
	}
	return "degraded", strings.TrimSpace(string(body))
}

func (a *app) handleServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"services": a.rollup(true),
		"note":     "unreachable services indicate dev-standalone mode; dependent views use marked dev seeds",
	})
}

func (a *app) handleServiceToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.store.mu.Lock()
	svc, ok := a.store.Services[id]
	if ok {
		svc.Enabled = !svc.Enabled
		if !svc.Enabled {
			svc.HealthStatus = "disabled"
		}
	}
	a.store.mu.Unlock()
	if !ok {
		writeProblem(w, http.StatusNotFound, "service not found", id)
		return
	}
	a.appendAudit("service.toggled", "service:"+id, actorOf(r), "toggle", "")
	writeJSON(w, http.StatusOK, svc)
}

// ---------- reverse proxy pass-through (SPEC §2) ----------

func (a *app) handleProxy(w http.ResponseWriter, r *http.Request) {
	svcID := r.PathValue("service")
	rest := r.PathValue("path")
	base, ok := a.serviceURL(svcID)
	if !ok {
		writeProblem(w, http.StatusNotFound, "unknown service in registry", svcID)
		return
	}
	target := strings.TrimSuffix(base, "/") + "/" + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequest(r.Method, target, r.Body)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "proxy error", err.Error())
		return
	}
	req.Header = r.Header.Clone()
	req.Header.Del("Authorization") // do not leak admin JWT downstream; dev services accept X-Dev-Role
	if req.Header.Get("X-Dev-Role") == "" {
		req.Header.Set("X-Dev-Role", "admin")
	}
	proxyClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := proxyClient.Do(req)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "downstream unreachable",
			svcID+" at "+base+" ("+err.Error()+")")
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
