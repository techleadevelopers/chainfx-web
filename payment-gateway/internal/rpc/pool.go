// Package rpc provides a resilient BSC/EVM RPC pool with automatic round-robin
// rotation, per-node circuit breaking, and background health checks.
package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"payment-gateway/internal/resilience"
)

// Node is one RPC endpoint with its health state and circuit breaker.
type Node struct {
	URL      string
	cb       *resilience.CircuitBreaker
	healthy  atomic.Bool
	disabled atomic.Bool
}

func newNode(url string) *Node {
	label := url
	if len(label) > 32 {
		label = label[:32]
	}
	n := &Node{URL: url}
	n.cb = resilience.NewCircuitBreaker("rpc:"+label, resilience.CBConfig{
		MaxFailures:     5,
		ResetTimeout:    60 * time.Second,
		HalfOpenSuccess: 2,
		IgnoreError: func(err error) bool {
			return !rpcRetryable(err)
		},
	})
	n.healthy.Store(true)
	return n
}

// Pool is a resilient multi-node RPC pool.
type Pool struct {
	nodes []*Node
	idx   atomic.Uint64
	mu    sync.RWMutex
}

// NewPool builds a pool from a comma-separated RPC URL list.
func NewPool(rawURLs string) (*Pool, error) {
	urls := splitURLs(rawURLs)
	if len(urls) == 0 {
		return nil, fmt.Errorf("rpc pool: no URLs provided")
	}
	p := &Pool{}
	for _, u := range urls {
		p.nodes = append(p.nodes, newNode(u))
	}
	return p, nil
}

// Do executes fn against a healthy node, rotating on failure with exponential backoff.
func (p *Pool) Do(ctx context.Context, fn func(*ethclient.Client) error) error {
	cfg := resilience.FastRetryConfig()
	cfg.MaxAttempts = len(p.nodes)*2 + 1
	if cfg.MaxAttempts < 3 {
		cfg.MaxAttempts = 3
	}

	var lastNode *Node

	return resilience.DoWithContext(ctx, cfg, "rpc.pool", rpcRetryable, func(ctx context.Context) error {
		node := p.pickExcluding(lastNode)
		lastNode = node

		var callErr error

		cbErr := node.cb.Execute(func() error {
			c, err := ethclient.DialContext(ctx, node.URL)
			if err != nil {
				node.healthy.Store(false)
				callErr = err

				slog.Warn("RPC dial failed",
					"url", truncate(node.URL, 60),
					"err", err,
				)

				return err
			}

			defer c.Close()

			callErr = fn(c)

			if callErr != nil {
				if rpcCapacityExhausted(callErr) && node.disabled.CompareAndSwap(false, true) {
					node.healthy.Store(false)
					slog.Warn("RPC node disabled: provider capacity exhausted",
						"url", truncate(node.URL, 60),
						"err", callErr,
					)
				}

				slog.Warn("RPC request failed",
					"url", truncate(node.URL, 60),
					"err", callErr,
				)

				// não mata o node no primeiro erro
				// o circuit breaker já controla falhas
			} else {
				node.healthy.Store(true)
			}

			return callErr
		})

		if cbErr != nil && callErr == nil {
			slog.Warn("RPC circuit breaker error",
				"url", truncate(node.URL, 60),
				"err", cbErr,
			)

			return cbErr
		}

		return callErr
	})
}

// BlockNumber returns the latest block number from any healthy node.
func (p *Pool) BlockNumber(ctx context.Context) (uint64, error) {
	var out uint64
	err := p.Do(ctx, func(c *ethclient.Client) error {
		n, err := c.BlockNumber(ctx)
		if err != nil {
			return err
		}
		out = n
		return nil
	})
	return out, err
}

// FilterLogs returns EVM logs matching the filter query.
func (p *Pool) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	var logs []types.Log
	err := p.Do(ctx, func(c *ethclient.Client) error {
		l, err := c.FilterLogs(ctx, q)
		if err != nil {
			return err
		}
		logs = l
		return nil
	})
	return logs, err
}

// BalanceAt returns the native coin balance of an address.
func (p *Pool) BalanceAt(ctx context.Context, addr common.Address) (*big.Int, error) {
	var bal *big.Int
	err := p.Do(ctx, func(c *ethclient.Client) error {
		b, err := c.BalanceAt(ctx, addr, nil)
		if err != nil {
			return err
		}
		bal = b
		return nil
	})
	return bal, err
}

// StartHealthChecks probes all nodes on a fixed interval via a background goroutine.
func (p *Pool) StartHealthChecks(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.probeAll(ctx)
			}
		}
	}()
}

func (p *Pool) probeAll(ctx context.Context) {
	p.mu.RLock()
	nodes := make([]*Node, len(p.nodes))
	copy(nodes, p.nodes)
	p.mu.RUnlock()

	for _, n := range nodes {
		go func(n *Node) {
			if n.disabled.Load() {
				return
			}

			pCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()

			c, err := ethclient.DialContext(pCtx, n.URL)
			if err != nil {
				n.healthy.Store(false)
				slog.Warn("RPC probe dial failed",
					"url", truncate(n.URL, 40),
					"err", err,
				)
				return
			}

			_, err = c.BlockNumber(pCtx)
			c.Close()

			if err != nil {
				n.healthy.Store(false)
				if rpcCapacityExhausted(err) && n.disabled.CompareAndSwap(false, true) {
					slog.Warn("RPC node disabled: provider capacity exhausted",
						"url", truncate(n.URL, 40),
						"err", err,
					)
					return
				}
				slog.Warn("RPC probe block failed",
					"url", truncate(n.URL, 40),
					"err", err,
				)
				return
			}

			if !n.healthy.Load() {
				slog.Info("RPC node recovered",
					"url", truncate(n.URL, 40),
				)
			}

			n.healthy.Store(true)

		}(n)
	}
}

// Health returns a per-node health snapshot (URL→healthy).
func (p *Pool) Health() map[string]bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]bool, len(p.nodes))
	for _, n := range p.nodes {
		out[truncate(n.URL, 40)] = n.healthy.Load()
	}
	return out
}

// ─── internal helpers ─────────────────────────────────────────────────────────

func (p *Pool) pickExcluding(exclude *Node) *Node {
	p.mu.RLock()
	nodes := p.nodes
	p.mu.RUnlock()

	if len(nodes) == 0 {
		return &Node{URL: "https://bsc-dataseed.binance.org/"}
	}
	for range nodes {
		i := p.idx.Add(1) % uint64(len(nodes))
		n := nodes[i]
		if n == exclude && len(nodes) > 1 {
			continue
		}
		if !n.disabled.Load() && n.healthy.Load() && n.cb.GetState() != resilience.StateOpen {
			return n
		}
	}
	for _, n := range nodes {
		if !n.disabled.Load() && n != exclude {
			slog.Warn("RPC pool: all enabled nodes unhealthy, using fallback node",
				"url", truncate(n.URL, 40),
			)
			return n
		}
	}
	// All unhealthy → use first as last-resort fallback
	slog.Warn("RPC pool: all nodes unhealthy, using first node as fallback")
	return nodes[0]
}

func splitURLs(raw string) []string {
	var out []string
	for _, u := range strings.Split(raw, ",") {
		if u = strings.TrimSpace(u); u != "" {
			out = append(out, u)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func rpcRetryable(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())

	if strings.Contains(msg, "invalid block range") ||
		strings.Contains(msg, "block range params") ||
		strings.Contains(msg, "invalid argument") {
		return false
	}

	if strings.Contains(msg, "archive") ||
		strings.Contains(msg, "debug") ||
		strings.Contains(msg, "trace") {
		return false
	}

	return true
}

func rpcCapacityExhausted(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "402") ||
		strings.Contains(msg, "payment required") ||
		strings.Contains(msg, "out of cu") ||
		strings.Contains(msg, "out of compute") ||
		strings.Contains(msg, "capacity limit exceeded") ||
		strings.Contains(msg, "monthly capacity limit exceeded") ||
		strings.Contains(msg, "quota exceeded") ||
		strings.Contains(msg, "billing")
}
