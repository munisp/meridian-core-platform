// loadsmoke — minimal, dependency-free HTTP load smoke harness used for the
// Meridian performance baseline (docs/perf-baseline.md).
//
// It drives a fixed number of concurrent workers against one URL for a fixed
// duration, then reports RPS, p50/p95/p99 latency, and status-code counts,
// and enforces the budget gate: p95 <= -p95-budget-ms AND zero 5xx.
//
// Example:
//
//	go run ./services/admin-api/perf/harness \
//	  -name admin-api-healthz -url http://127.0.0.1:8095/healthz \
//	  -duration 60s -concurrency 50 -p95-budget-ms 200
//
//	go run ./services/admin-api/perf/harness \
//	  -name admin-api-overview -url http://127.0.0.1:8095/v1/admin/overview \
//	  -header "X-Dev-Role: admin" -duration 60s -concurrency 50 -p95-budget-ms 250
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	var (
		name        = flag.String("name", "target", "label for the results table")
		url         = flag.String("url", "", "target URL")
		method      = flag.String("method", "GET", "HTTP method")
		header      = flag.String("header", "", "optional request header 'K: V'")
		duration    = flag.Duration("duration", 60*time.Second, "measurement window")
		concurrency = flag.Int("concurrency", 50, "concurrent workers")
		warmup      = flag.Duration("warmup", 3*time.Second, "warmup before the measurement window")
		p95budget   = flag.Int64("p95-budget-ms", 200, "budget gate: max p95 latency in ms")
	)
	flag.Parse()
	if *url == "" {
		fmt.Fprintln(os.Stderr, "-url is required")
		os.Exit(2)
	}

	tr := &http.Transport{
		MaxIdleConns:        *concurrency * 2,
		MaxIdleConnsPerHost: *concurrency * 2,
	}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	var mu sync.Mutex
	lat := make([]time.Duration, 0, 1<<20)
	statuses := map[int]int{}
	var reqTotal int64
	var errNet int64

	one := func() {
		req, err := http.NewRequest(*method, *url, nil)
		if err != nil {
			panic(err)
		}
		if *header != "" {
			k, v, _ := strings.Cut(*header, ":")
			req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			atomic.AddInt64(&errNet, 1)
			atomic.AddInt64(&reqTotal, 1)
			return
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		d := time.Since(start)
		atomic.AddInt64(&reqTotal, 1)
		mu.Lock()
		lat = append(lat, d)
		statuses[resp.StatusCode]++
		mu.Unlock()
	}

	// warmup
	stop := make(chan struct{})
	var wg sync.WaitGroup
	run := func(done chan struct{}) {
		for i := 0; i < *concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-done:
						return
					default:
						one()
					}
				}
			}()
		}
	}
	warm := make(chan struct{})
	run(warm)
	time.Sleep(*warmup)
	close(warm)
	wg.Wait()

	// measurement window
	lat = lat[:0]
	statuses = map[int]int{}
	atomic.StoreInt64(&reqTotal, 0)
	atomic.StoreInt64(&errNet, 0)
	start := time.Now()
	run(stop)
	time.Sleep(*duration)
	close(stop)
	wg.Wait()
	elapsed := time.Since(start)

	mu.Lock()
	defer mu.Unlock()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		i := int(p * float64(len(lat)))
		if i >= len(lat) {
			i = len(lat) - 1
		}
		return lat[i]
	}
	rps := float64(reqTotal) / elapsed.Seconds()
	var s5xx, s4xx, s2xx, other int
	for code, n := range statuses {
		switch {
		case code >= 500:
			s5xx += n
		case code >= 400:
			s4xx += n
		case code >= 200 && code < 300:
			s2xx += n
		default:
			other += n
		}
	}

	fmt.Printf("name=%s url=%s duration=%s concurrency=%d\n", *name, *url, *duration, *concurrency)
	fmt.Printf("requests=%d rps=%.1f net_errors=%d\n", reqTotal, rps, errNet)
	fmt.Printf("latency p50=%s p95=%s p99=%s max=%s\n",
		pct(0.50).Round(time.Microsecond), pct(0.95).Round(time.Microsecond),
		pct(0.99).Round(time.Microsecond), lat[len(lat)-1].Round(time.Microsecond))
	fmt.Printf("status 2xx=%d 4xx=%d 5xx=%d other=%d\n", s2xx, s4xx, s5xx, other)

	p95ms := pct(0.95).Milliseconds()
	gateOK := true
	if p95ms > *p95budget {
		fmt.Printf("GATE FAIL: p95=%dms exceeds budget %dms\n", p95ms, *p95budget)
		gateOK = false
	}
	if s5xx > 0 {
		fmt.Printf("GATE FAIL: %d 5xx responses\n", s5xx)
		gateOK = false
	}
	if errNet > 0 {
		fmt.Printf("GATE FAIL: %d network errors\n", errNet)
		gateOK = false
	}
	if gateOK {
		fmt.Printf("GATE PASS: p95=%dms<=%dms, 5xx=0, net_errors=0\n", p95ms, *p95budget)
	} else {
		os.Exit(1)
	}
}
