// Package news fetches and caches crypto news from public RSS feeds.
// It exposes a single HTTP handler for GET /api/public/news.
package news

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ── Asset definitions ────────────────────────────────────────────────────────

type assetDef struct {
	Symbol   string
	Name     string
	Keywords []string
}

var trackedAssets = []assetDef{
	{Symbol: "BTC", Name: "Bitcoin", Keywords: []string{"bitcoin", "btc"}},
	{Symbol: "ETH", Name: "Ethereum", Keywords: []string{"ethereum"}},
	{Symbol: "SOL", Name: "Solana", Keywords: []string{"solana"}},
	{Symbol: "XRP", Name: "XRP", Keywords: []string{"xrp", "ripple"}},
	{Symbol: "USDC", Name: "USD Coin", Keywords: []string{"usdc", "usd coin"}},
	{Symbol: "AVAX", Name: "Avalanche", Keywords: []string{"avalanche", "avax"}},
	{Symbol: "UNI", Name: "Uniswap", Keywords: []string{"uniswap"}},
	{Symbol: "DOGE", Name: "Dogecoin", Keywords: []string{"dogecoin", "doge"}},
}

// ── RSS feed sources ─────────────────────────────────────────────────────────

type feedSource struct {
	Name string
	URL  string
}

var feedSources = []feedSource{
	{Name: "CoinDesk", URL: "https://www.coindesk.com/arc/outboundfeeds/rss/"},
	{Name: "CoinTelegraph", URL: "https://cointelegraph.com/rss"},
	{Name: "Decrypt", URL: "https://decrypt.co/feed"},
}

// ── Public types ─────────────────────────────────────────────────────────────

// NewsItem is a normalised, asset-classified news article.
type NewsItem struct {
	ID          string    `json:"id"`
	Asset       string    `json:"asset"`
	AssetName   string    `json:"asset_name"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary"`
	Source      string    `json:"source"`
	SourceURL   string    `json:"source_url"`
	PublishedAt time.Time `json:"published_at"`
	Category    string    `json:"category"`
}

// ── RSS parsing ──────────────────────────────────────────────────────────────

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	GUID        string `xml:"guid"`
}

type rssFeed struct {
	Items []rssItem `xml:"channel>item"`
}

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

func stripTags(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	// collapse whitespace
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func classify(title, desc string) (symbol, name string) {
	text := strings.ToLower(title + " " + desc)
	for _, a := range trackedAssets {
		for _, kw := range a.Keywords {
			if strings.Contains(text, kw) {
				return a.Symbol, a.Name
			}
		}
	}
	return "BTC", "Bitcoin" // generic crypto fallback
}

// ── Manager ──────────────────────────────────────────────────────────────────

// Manager fetches RSS feeds in the background and caches normalised items.
type Manager struct {
	mu        sync.RWMutex
	items     []NewsItem
	updatedAt time.Time
	client    *http.Client
}

// New creates a Manager and starts its background refresh goroutine.
func New() *Manager {
	m := &Manager{
		client: &http.Client{Timeout: 12 * time.Second},
	}
	go m.StartRefresher(context.Background())
	return m
}

// StartRefresher fetches immediately then polls every 3 minutes.
func (m *Manager) StartRefresher(ctx context.Context) {
	m.refresh()
	ticker := time.NewTicker(3 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.refresh()
		}
	}
}

func (m *Manager) refresh() {
	items := m.fetchAll()
	m.mu.Lock()
	m.items = items
	m.updatedAt = time.Now()
	m.mu.Unlock()
}

func (m *Manager) fetchAll() []NewsItem {
	type result struct{ items []NewsItem }
	ch := make(chan result, len(feedSources))
	for _, src := range feedSources {
		src := src
		go func() {
			items, _ := m.fetchFeed(src)
			ch <- result{items}
		}()
	}
	var all []NewsItem
	seen := make(map[string]bool)
	for range feedSources {
		r := <-ch
		for _, item := range r.items {
			key := item.SourceURL
			if key == "" {
				key = item.Title
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, item)
		}
	}
	// Insertion sort descending by publication time (small N, simple is fine).
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].PublishedAt.After(all[j-1].PublishedAt); j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	return all
}

func (m *Manager) fetchFeed(src feedSource) ([]NewsItem, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ChainFX-NewsBot/1.0 (+https://chainfx.com)")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2 MB max
	if err != nil {
		return nil, err
	}
	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, err
	}

	const maxPerFeed = 20
	var items []NewsItem
	for i, raw := range feed.Items {
		if i >= maxPerFeed {
			break
		}
		title := strings.TrimSpace(raw.Title)
		if title == "" {
			continue
		}
		link := strings.TrimSpace(raw.Link)
		if link == "" || !strings.HasPrefix(link, "http") {
			continue
		}
		summary := stripTags(raw.Description)
		if len(summary) > 300 {
			summary = summary[:300]
		}

		sym, name := classify(title, summary)

		pub, _ := time.Parse(time.RFC1123Z, raw.PubDate)
		if pub.IsZero() {
			pub, _ = time.Parse(time.RFC1123, raw.PubDate)
		}
		if pub.IsZero() {
			pub = time.Now()
		}

		id := raw.GUID
		if id == "" {
			id = fmt.Sprintf("%s-%d", src.Name, i)
		}

		items = append(items, NewsItem{
			ID:          id,
			Asset:       sym,
			AssetName:   name,
			Title:       title,
			Summary:     summary,
			Source:      src.Name,
			SourceURL:   link,
			PublishedAt: pub,
			Category:    "Market",
		})
	}
	return items, nil
}

// ── HTTP handler ─────────────────────────────────────────────────────────────

// Handle serves GET /api/public/news?asset=BTC&limit=40
func (m *Manager) Handle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	asset := strings.ToUpper(strings.TrimSpace(q.Get("asset")))
	limit := parseLimit(q.Get("limit"), 40, 100)

	m.mu.RLock()
	all := m.items
	updatedAt := m.updatedAt
	m.mu.RUnlock()

	var items []NewsItem
	for _, it := range all {
		if asset != "" && it.Asset != asset {
			continue
		}
		items = append(items, it)
		if len(items) >= limit {
			break
		}
	}
	if items == nil {
		items = []NewsItem{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":      items,
		"updated_at": updatedAt,
	})
}

func parseLimit(s string, def, max int) int {
	if s == "" {
		return def
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}
