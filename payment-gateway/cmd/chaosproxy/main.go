// chaosproxy is a small, dependency-free network fault injector used only
// by tests/chaos_suite.sh. It sits between the real API process and a real
// downstream (Postgres for -mode=tcp, an RPC/PSP stub for -mode=http) so the
// chaos suite can inject real infrastructure failures — latency, dropped
// connections, forced errors — without Docker, docker-compose, or `tc`
// (none of which are available/supported in this environment).
//
// This binary is test-only tooling. It is never wired into cmd/api and
// carries no production import path.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

type chaosState struct {
	latencyMs int64 // atomic: ms of latency added per connection/request
	errorRate int64 // atomic: integer 0-100, http mode only — % of requests forced to 503

	mu      sync.Mutex
	killGen int // incremented by /chaos/kill; tcp mode closes conns from a lower gen
}

func (c *chaosState) latency() time.Duration {
	return time.Duration(atomic.LoadInt64(&c.latencyMs)) * time.Millisecond
}

func (c *chaosState) shouldForceError() bool {
	rate := atomic.LoadInt64(&c.errorRate)
	if rate <= 0 {
		return false
	}
	if rate >= 100 {
		return true
	}
	return time.Now().UnixNano()%100 < rate
}

func (c *chaosState) gen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.killGen
}

func (c *chaosState) bumpKillGen() {
	c.mu.Lock()
	c.killGen++
	c.mu.Unlock()
}

func main() {
	mode := flag.String("mode", "tcp", "tcp (Postgres/RPC TCP) or http (JSON-RPC/PSP webhook passthrough)")
	listen := flag.String("listen", ":15432", "address the proxy listens on (what the app under test connects to)")
	upstream := flag.String("upstream", "", "real upstream address (host:port for tcp, http(s)://host:port for http)")
	controlListen := flag.String("control", ":19100", "address for the chaos control API (latency/kill/error injection)")
	flag.Parse()

	if *upstream == "" {
		log.Fatal("chaosproxy: -upstream is required")
	}

	state := &chaosState{}

	go runControlAPI(*controlListen, state)

	switch *mode {
	case "tcp":
		runTCPProxy(*listen, *upstream, state)
	case "http":
		runHTTPProxy(*listen, *upstream, state)
	default:
		log.Fatalf("chaosproxy: unknown -mode %q (want tcp or http)", *mode)
	}
}

// ---------------------------------------------------------------------
// Control API — curl'd by tests/chaos_suite.sh mid-run to inject/clear
// faults in real time while k6 hammers the real app.
// ---------------------------------------------------------------------

func runControlAPI(addr string, state *chaosState) {
	mux := http.NewServeMux()

	mux.HandleFunc("/chaos/latency", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Ms int64 `json:"ms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		atomic.StoreInt64(&state.latencyMs, body.Ms)
		log.Printf("[chaos] latency set to %dms", body.Ms)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/chaos/error_rate", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Rate int64 `json:"rate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		atomic.StoreInt64(&state.errorRate, body.Rate)
		log.Printf("[chaos] forced error rate set to %d%%", body.Rate)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/chaos/kill", func(w http.ResponseWriter, r *http.Request) {
		state.bumpKillGen()
		log.Printf("[chaos] kill signal issued — dropping all active connections")
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/chaos/reset", func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt64(&state.latencyMs, 0)
		atomic.StoreInt64(&state.errorRate, 0)
		log.Printf("[chaos] state reset (latency=0, error_rate=0)")
		w.WriteHeader(http.StatusOK)
	})

	log.Printf("[chaos] control API listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("chaos control API failed: %v", err)
	}
}

// ---------------------------------------------------------------------
// TCP mode — used for Postgres. Simulates "queda de banco" (kill) and
// network lag (latency) by proxying raw bytes and periodically checking
// whether a /chaos/kill happened after this connection was accepted.
// ---------------------------------------------------------------------

func runTCPProxy(listen, upstream string, state *chaosState) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatalf("chaosproxy tcp: listen %s: %v", listen, err)
	}
	log.Printf("[chaos] tcp proxy %s -> %s", listen, upstream)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("chaosproxy tcp: accept error: %v", err)
			continue
		}
		go handleTCPConn(conn, upstream, state)
	}
}

func handleTCPConn(client net.Conn, upstream string, state *chaosState) {
	defer client.Close()
	connGen := state.gen()

	upConn, err := net.DialTimeout("tcp", upstream, 5*time.Second)
	if err != nil {
		log.Printf("[chaos] upstream dial failed: %v", err)
		return
	}
	defer upConn.Close()

	killWatch := func(stop <-chan struct{}) {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if state.gen() != connGen {
					client.Close()
					upConn.Close()
					return
				}
			}
		}
	}
	stop := make(chan struct{})
	defer close(stop)
	go killWatch(stop)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyWithLatency(upConn, client, state) }()
	go func() { defer wg.Done(); copyWithLatency(client, upConn, state) }()
	wg.Wait()
}

func copyWithLatency(dst, src net.Conn, state *chaosState) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if d := state.latency(); d > 0 {
				time.Sleep(d)
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// ---------------------------------------------------------------------
// HTTP mode — used for RPC (BSC/Polygon) and PSP (Efí) stub endpoints.
// Injects latency and forced 503s ("mTLS/upstream lost") on real HTTP
// round-trips through httputil.ReverseProxy.
// ---------------------------------------------------------------------

func runHTTPProxy(listen, upstream string, state *chaosState) {
	target, err := url.Parse(upstream)
	if err != nil {
		log.Fatalf("chaosproxy http: bad -upstream %q: %v", upstream, err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	baseDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		baseDirector(req)
		req.Host = target.Host
	}

	handler := func(w http.ResponseWriter, r *http.Request) {
		if state.shouldForceError() {
			http.Error(w, `{"error":"chaos: upstream unavailable (simulated)"}`, http.StatusServiceUnavailable)
			return
		}
		if d := state.latency(); d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}

	log.Printf("[chaos] http proxy %s -> %s", listen, upstream)
	srv := &http.Server{Addr: listen, Handler: http.HandlerFunc(handler)}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("chaosproxy http: %v", err)
	}
}
