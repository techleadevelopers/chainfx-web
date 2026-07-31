package mobile

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type stayAPIClient struct {
	client *http.Client
}

type stayAPIError struct {
	Code       string
	HTTPStatus int
	Message    string
}

func (e stayAPIError) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

type stayHotelProduct struct {
	ID           string         `json:"id"`
	ProductID    string         `json:"product_id"`
	Provider     string         `json:"provider"`
	OrderType    string         `json:"order_type"`
	ProductKind  string         `json:"product_kind"`
	Title        string         `json:"title"`
	Brand        string         `json:"brand"`
	Location     string         `json:"location"`
	CountryCode  string         `json:"country_code"`
	Currency     string         `json:"currency"`
	Categories   []string       `json:"categories"`
	ProductType  string         `json:"product_type"`
	LogoURL      string         `json:"logo_url"`
	ImageURL     string         `json:"image_url"`
	Rating       string         `json:"rating,omitempty"`
	ReviewScore  string         `json:"review_score,omitempty"`
	Price        string         `json:"price,omitempty"`
	RoomType     string         `json:"room_type,omitempty"`
	Available    bool           `json:"available"`
	BookingLinks map[string]any `json:"booking_links,omitempty"`
}

type stayHotelMetricsCounters struct {
	cacheHit          atomic.Uint64
	cacheMiss         atomic.Uint64
	cacheStaleHit     atomic.Uint64
	providerRequests  atomic.Uint64
	providerErrors402 atomic.Uint64
	providerErrors429 atomic.Uint64
}

var (
	stayHotelCacheSchemaMu    sync.Mutex
	stayHotelCacheSchemaReady sync.Map
	stayHotelRequestLocks     sync.Map
	stayHotelMetrics          stayHotelMetricsCounters
)

func newStayAPIClient() *stayAPIClient {
	timeout := time.Duration(envInt("STAYAPI_TIMEOUT_SECONDS", 10)) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &stayAPIClient{client: &http.Client{Timeout: timeout}}
}

func stayAPIEnabled() bool {
	return strings.TrimSpace(os.Getenv("STAYAPI_API_KEY")) != ""
}

func stayAPIBaseURL() string {
	if value := strings.TrimRight(strings.TrimSpace(os.Getenv("STAYAPI_BASE_URL")), "/"); value != "" {
		return value
	}
	return "https://api.stayapi.com"
}

func (c *stayAPIClient) get(ctx context.Context, path string, query url.Values, out any) error {
	if !stayAPIEnabled() {
		return stayAPIError{Code: "stayapi_not_configured", Message: "StayAPI key not configured"}
	}
	endpoint := stayAPIBaseURL() + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", strings.TrimSpace(os.Getenv("STAYAPI_API_KEY")))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ChainFX-Mobile-Travel/1.0")
	res, err := c.client.Do(req)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return stayAPIError{Code: "provider_timeout", Message: "StayAPI request timeout"}
		}
		if ctx.Err() != nil {
			return stayAPIError{Code: "provider_timeout", Message: "StayAPI request cancelled"}
		}
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return stayAPIError{Code: stayAPIHTTPErrorCode(res.StatusCode), HTTPStatus: res.StatusCode, Message: stayAPISafeMessage(raw)}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *stayAPIClient) SearchHotels(ctx context.Context, q stayHotelQuery) ([]stayHotelProduct, error) {
	values := url.Values{}
	values.Set("location", firstNonEmptyStr(q.Location, "Brazil"))
	if q.Search != "" {
		values.Set("q", q.Search)
		values.Set("query", q.Search)
		values.Set("hotel_name", q.Search)
	}
	values.Set("check_in", firstNonEmptyStr(q.CheckIn, time.Now().UTC().AddDate(0, 0, 30).Format("2006-01-02")))
	values.Set("check_out", firstNonEmptyStr(q.CheckOut, time.Now().UTC().AddDate(0, 0, 32).Format("2006-01-02")))
	values.Set("adults", firstNonEmptyStr(q.Adults, "2"))
	if q.Children != "" {
		values.Set("children", q.Children)
	}
	if q.Rooms != "" {
		values.Set("rooms", q.Rooms)
	}
	values.Set("currency", firstNonEmptyStr(q.Currency, "BRL"))
	if q.Locale != "" {
		values.Set("locale", q.Locale)
	}
	var decoded map[string]any
	path := firstNonEmptyStr(os.Getenv("STAYAPI_HOTEL_SEARCH_PATH"), "/v1/google_hotels/search")
	if err := c.get(ctx, path, values, &decoded); err != nil {
		return nil, err
	}
	hotels := normalizeStayHotels(decoded, q)
	if len(hotels) == 0 && q.Search != "" {
		meta := url.Values{}
		meta.Set("hotel_name", q.Search)
		meta.Set("location", firstNonEmptyStr(q.Location, "Brazil"))
		var metaDecoded map[string]any
		if err := c.get(ctx, "/v1/meta/search", meta, &metaDecoded); err == nil {
			hotels = normalizeStayHotels(metaDecoded, q)
		}
	}
	return hotels, nil
}

type stayHotelQuery struct {
	Location string
	Search   string
	CheckIn  string
	CheckOut string
	Adults   string
	Children string
	Rooms    string
	Currency string
	Locale   string
}

func normalizeStayHotelQuery(q stayHotelQuery) stayHotelQuery {
	now := time.Now().UTC()
	q.Location = firstNonEmptyStr(strings.TrimSpace(q.Location), "Brazil")
	q.Search = strings.TrimSpace(q.Search)
	q.CheckIn = firstNonEmptyStr(strings.TrimSpace(q.CheckIn), now.AddDate(0, 0, 30).Format("2006-01-02"))
	q.CheckOut = firstNonEmptyStr(strings.TrimSpace(q.CheckOut), now.AddDate(0, 0, 32).Format("2006-01-02"))
	q.Adults = firstNonEmptyStr(strings.TrimSpace(q.Adults), "2")
	q.Children = firstNonEmptyStr(strings.TrimSpace(q.Children), "0")
	q.Rooms = firstNonEmptyStr(strings.TrimSpace(q.Rooms), "1")
	q.Currency = strings.ToUpper(firstNonEmptyStr(strings.TrimSpace(q.Currency), "BRL"))
	q.Locale = firstNonEmptyStr(strings.TrimSpace(q.Locale), "pt-BR")
	return q
}

func stayHotelCacheKey(q stayHotelQuery) string {
	q = normalizeStayHotelQuery(q)
	raw := strings.Join([]string{
		strings.ToLower(q.Location),
		strings.ToLower(q.Search),
		q.CheckIn,
		q.CheckOut,
		q.Adults,
		q.Children,
		q.Rooms,
		q.Currency,
		q.Locale,
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum[:])
}

func stayHotelCacheFreshTTL() time.Duration {
	hours := envInt("STAYAPI_CACHE_TTL_HOURS", 24)
	if hours <= 0 {
		hours = 24
	}
	return time.Duration(hours) * time.Hour
}

func stayHotelCacheStaleTTL() time.Duration {
	days := envInt("STAYAPI_STALE_CACHE_DAYS", 30)
	if days <= 0 {
		days = 30
	}
	return time.Duration(days) * 24 * time.Hour
}

func observeStayHotelMetric(event string, err error) {
	switch event {
	case "cache_hit":
		stayHotelMetrics.cacheHit.Add(1)
	case "cache_miss":
		stayHotelMetrics.cacheMiss.Add(1)
	case "cache_stale_hit":
		stayHotelMetrics.cacheStaleHit.Add(1)
	case "stayapi_request":
		stayHotelMetrics.providerRequests.Add(1)
	case "stayapi_error":
		if providerErr, ok := err.(stayAPIError); ok {
			switch providerErr.HTTPStatus {
			case http.StatusPaymentRequired:
				stayHotelMetrics.providerErrors402.Add(1)
			case http.StatusTooManyRequests:
				stayHotelMetrics.providerErrors429.Add(1)
			}
		}
	}
	if err != nil {
		slog.Debug("stayapi_hotels_metric", "event", event, "error_code", stayAPIErrorCode(err))
		return
	}
	slog.Debug("stayapi_hotels_metric", "event", event)
}

func lockStayHotelCacheKey(key string) func() {
	actual, _ := stayHotelRequestLocks.LoadOrStore(key, &sync.Mutex{})
	mu := actual.(*sync.Mutex)
	mu.Lock()
	return func() {
		mu.Unlock()
	}
}

type stayHotelCacheHit struct {
	Items     []stayHotelProduct
	ExpiresAt time.Time
	Stale     bool
}

func ensureStayHotelCacheSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	if _, ok := stayHotelCacheSchemaReady.Load(db); ok {
		return nil
	}
	stayHotelCacheSchemaMu.Lock()
	defer stayHotelCacheSchemaMu.Unlock()
	if _, ok := stayHotelCacheSchemaReady.Load(db); ok {
		return nil
	}
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS stayapi_hotel_search_cache (
  cache_key TEXT PRIMARY KEY,
  query JSONB NOT NULL,
  payload JSONB NOT NULL,
  provider TEXT NOT NULL DEFAULT 'stayapi',
  expires_at TIMESTAMPTZ NOT NULL,
  stale_until TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_stayapi_hotel_search_cache_stale_until
ON stayapi_hotel_search_cache(stale_until)`)
	if err != nil {
		return err
	}
	stayHotelCacheSchemaReady.Store(db, true)
	if err := cleanupExpiredStayHotelCache(ctx, db); err != nil {
		slog.Warn("stayapi hotel cache cleanup warning", "error", err)
	}
	return nil
}

func cleanupExpiredStayHotelCache(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.ExecContext(ctx, `
DELETE FROM stayapi_hotel_search_cache
WHERE stale_until < NOW()`)
	return err
}

func (s *Server) startStayHotelCacheJanitor(ctx context.Context) {
	if s == nil || s.db == nil || s.db.SQL == nil {
		return
	}
	minutes := envInt("STAYAPI_CACHE_CLEANUP_MINUTES", 360)
	if minutes <= 0 {
		minutes = 360
	}
	ticker := time.NewTicker(time.Duration(minutes) * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := ensureStayHotelCacheSchema(ctx, s.db.SQL); err != nil {
				slog.Warn("stayapi hotel cache schema warning", "error", err)
				continue
			}
			if err := cleanupExpiredStayHotelCache(ctx, s.db.SQL); err != nil {
				slog.Warn("stayapi hotel cache cleanup warning", "error", err)
			}
		}
	}
}

func (s *Server) getStayHotelCache(ctx context.Context, key string, allowStale bool) (stayHotelCacheHit, bool) {
	var hit stayHotelCacheHit
	if s == nil || s.db == nil || s.db.SQL == nil || strings.TrimSpace(key) == "" {
		return hit, false
	}
	if err := ensureStayHotelCacheSchema(ctx, s.db.SQL); err != nil {
		return hit, false
	}
	var raw string
	var expiresAt, staleUntil time.Time
	err := s.db.SQL.QueryRowContext(ctx, `
SELECT payload::text, expires_at, stale_until
FROM stayapi_hotel_search_cache
WHERE cache_key=$1 AND ($2::boolean OR expires_at > NOW()) AND stale_until > NOW()`, key, allowStale).
		Scan(&raw, &expiresAt, &staleUntil)
	if err != nil {
		return hit, false
	}
	if err := json.Unmarshal([]byte(raw), &hit.Items); err != nil || len(hit.Items) == 0 {
		return stayHotelCacheHit{}, false
	}
	hit.ExpiresAt = expiresAt
	hit.Stale = time.Now().UTC().After(expiresAt)
	_ = staleUntil
	return hit, true
}

func (s *Server) setStayHotelCache(ctx context.Context, key string, q stayHotelQuery, hotels []stayHotelProduct) {
	if s == nil || s.db == nil || s.db.SQL == nil || strings.TrimSpace(key) == "" || len(hotels) == 0 {
		return
	}
	if err := ensureStayHotelCacheSchema(ctx, s.db.SQL); err != nil {
		return
	}
	queryJSON, _ := json.Marshal(normalizeStayHotelQuery(q))
	payloadJSON, _ := json.Marshal(hotels)
	freshTTL := stayHotelCacheFreshTTL()
	staleTTL := stayHotelCacheStaleTTL()
	_, _ = s.db.SQL.ExecContext(ctx, `
INSERT INTO stayapi_hotel_search_cache (cache_key, query, payload, expires_at, stale_until, updated_at)
VALUES ($1, $2::jsonb, $3::jsonb, NOW() + $4::interval, NOW() + $5::interval, NOW())
ON CONFLICT (cache_key) DO UPDATE SET
  query=EXCLUDED.query,
  payload=EXCLUDED.payload,
  expires_at=EXCLUDED.expires_at,
  stale_until=EXCLUDED.stale_until,
  updated_at=NOW()`,
		key, string(queryJSON), string(payloadJSON), fmt.Sprintf("%d seconds", int(freshTTL.Seconds())), fmt.Sprintf("%d seconds", int(staleTTL.Seconds())))
}

func (s *Server) handleTravelHotels(w http.ResponseWriter, r *http.Request) {
	if !stayAPIEnabled() {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "provider": "stayapi", "configured": false})
		return
	}
	query := normalizeStayHotelQuery(stayHotelQuery{
		Location: strings.TrimSpace(r.URL.Query().Get("location")),
		Search:   firstNonEmptyStr(strings.TrimSpace(r.URL.Query().Get("search")), strings.TrimSpace(r.URL.Query().Get("q"))),
		CheckIn:  strings.TrimSpace(r.URL.Query().Get("check_in")),
		CheckOut: strings.TrimSpace(r.URL.Query().Get("check_out")),
		Adults:   strings.TrimSpace(r.URL.Query().Get("adults")),
		Children: strings.TrimSpace(r.URL.Query().Get("children")),
		Rooms:    strings.TrimSpace(r.URL.Query().Get("rooms")),
		Currency: strings.TrimSpace(r.URL.Query().Get("currency")),
		Locale:   strings.TrimSpace(r.URL.Query().Get("locale")),
	})
	cacheKey := stayHotelCacheKey(query)
	if cached, ok := s.getStayHotelCache(r.Context(), cacheKey, false); ok {
		observeStayHotelMetric("cache_hit", nil)
		writeJSON(w, http.StatusOK, map[string]any{"items": cached.Items, "provider": "stayapi", "configured": true, "source": "cache", "cached": true, "stale": false})
		return
	}
	observeStayHotelMetric("cache_miss", nil)
	unlock := lockStayHotelCacheKey(cacheKey)
	defer unlock()
	if cached, ok := s.getStayHotelCache(r.Context(), cacheKey, false); ok {
		observeStayHotelMetric("cache_hit", nil)
		writeJSON(w, http.StatusOK, map[string]any{"items": cached.Items, "provider": "stayapi", "configured": true, "source": "cache", "cached": true, "stale": false})
		return
	}
	observeStayHotelMetric("stayapi_request", nil)
	hotels, err := newStayAPIClient().SearchHotels(r.Context(), query)
	if err != nil {
		observeStayHotelMetric("stayapi_error", err)
		if cached, ok := s.getStayHotelCache(r.Context(), cacheKey, true); ok {
			observeStayHotelMetric("cache_stale_hit", nil)
			writeJSON(w, http.StatusOK, map[string]any{"items": cached.Items, "provider": "stayapi", "configured": true, "source": "cache", "cached": true, "stale": true, "error_code": stayAPIErrorCode(err)})
			return
		}
		status := http.StatusBadGateway
		if providerErr, ok := err.(stayAPIError); ok && providerErr.HTTPStatus > 0 && providerErr.HTTPStatus < 500 {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]any{"error": "erro ao consultar StayAPI", "code": stayAPIErrorCode(err)})
		return
	}
	s.setStayHotelCache(r.Context(), cacheKey, query, hotels)
	writeJSON(w, http.StatusOK, map[string]any{"items": hotels, "provider": "stayapi", "configured": true, "source": "stayapi", "cached": false, "stale": false})
}

func (s *Server) handleTravelQuote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProductID string `json:"product_id"`
		HotelID   string `json:"hotel_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	productID := firstNonEmptyStr(strings.TrimSpace(req.ProductID), strings.TrimSpace(req.HotelID))
	if productID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "product_id obrigatorio"})
		return
	}
	if !strings.HasPrefix(productID, "stayapi_") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "produto de viagem invalido", "code": "INVALID_TRAVEL_PRODUCT", "product_id": productID})
		return
	}
	writeJSON(w, http.StatusNotImplemented, stayAPITravelBookingUnavailablePayload(productID))
}

func (s *Server) handleTravelOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProductID string `json:"product_id"`
		HotelID   string `json:"hotel_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	productID := firstNonEmptyStr(strings.TrimSpace(req.ProductID), strings.TrimSpace(req.HotelID))
	if productID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "product_id obrigatorio"})
		return
	}
	if !strings.HasPrefix(productID, "stayapi_") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "produto de viagem invalido", "code": "INVALID_TRAVEL_PRODUCT", "product_id": productID})
		return
	}
	writeJSON(w, http.StatusNotImplemented, stayAPITravelBookingUnavailablePayload(productID))
}

func stayAPITravelBookingUnavailablePayload(productID string) map[string]any {
	return map[string]any{
		"error":        "booking StayAPI ainda sem endpoint de compra habilitado",
		"code":         "TRAVEL_BOOKING_NOT_IMPLEMENTED",
		"provider":     "stayapi",
		"product_id":   productID,
		"order_type":   "travel",
		"product_type": "hotel",
		"product_kind": "hotel",
	}
}

func normalizeStayHotels(decoded map[string]any, q stayHotelQuery) []stayHotelProduct {
	items := stayHotelItems(decoded)
	out := make([]stayHotelProduct, 0, len(items))
	for index, item := range items {
		hotel := stayHotelFromMap(item, q, index)
		if hotel.ID == "" || hotel.Title == "" {
			continue
		}
		out = append(out, hotel)
		if len(out) >= 30 {
			break
		}
	}
	return out
}

func stayHotelItems(decoded map[string]any) []map[string]any {
	candidates := []any{decoded["data"], decoded["hotels"], decoded["results"], decoded["properties"]}
	var dataLinks map[string]any
	var dataMap map[string]any
	if data, ok := decoded["data"].(map[string]any); ok {
		dataMap = data
		candidates = append(candidates, data["hotels"], data["results"], data["properties"], data["items"])
		if links, ok := data["links"].(map[string]any); ok {
			dataLinks = links
		}
	}
	for _, candidate := range candidates {
		if rows, ok := candidate.([]any); ok {
			out := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				if item, ok := row.(map[string]any); ok {
					out = append(out, item)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	if links, ok := decoded["links"].(map[string]any); ok {
		return []map[string]any{dataWithLinks(decoded, links)}
	}
	if dataMap != nil && dataLinks != nil {
		return []map[string]any{dataWithLinks(dataMap, dataLinks)}
	}
	return nil
}

func dataWithLinks(data map[string]any, links map[string]any) map[string]any {
	out := make(map[string]any, len(data)+1)
	for k, v := range data {
		out[k] = v
	}
	out["links"] = links
	return out
}

func stayHotelFromMap(item map[string]any, q stayHotelQuery, index int) stayHotelProduct {
	location := firstNonEmptyStr(
		stayLocationText(item["location"]),
		stayLocationText(item["city"]),
		stayLocationText(item["destination"]),
		stayLocationText(item["geo"]),
		stayLocationText(item["coordinates"]),
		stayLocationText(item["address"]),
		q.Location,
	)
	name := firstNonEmptyStr(
		stayFirstString(item["hotel_name"]),
		stayFirstString(item["property_name"]),
		stayFirstString(item["name"]),
		stayFirstString(item["title"]),
		stayFirstString(item["display_name"]),
	)
	if name == "" {
		if location != "" {
			name = "Hotel em " + location
		} else {
			name = fmt.Sprintf("Hotel %02d", index+1)
		}
	}
	id := firstNonEmptyStr(
		stayFirstString(item["hotel_id"]),
		stayFirstString(item["property_id"]),
		stayFirstString(item["id"]),
		mobilePayHash(name + ":" + location)[:18],
	)
	image := firstNonEmptyStr(
		stayFirstString(item["images"]),
		stayFirstString(item["photos"]),
		stayFirstString(item["pictures"]),
		stayFirstString(item["image"]),
		stayFirstString(item["photo"]),
		stayFirstString(item["thumbnail"]),
		stayFirstString(item["image_url"]),
	)
	productID := "stayapi_" + id
	return stayHotelProduct{
		ID:           productID,
		ProductID:    productID,
		Provider:     "stayapi",
		OrderType:    "travel",
		ProductKind:  "hotel",
		Title:        name,
		Brand:        "Hotel Booking",
		Location:     location,
		CountryCode:  "BR",
		Currency:     firstNonEmptyStr(q.Currency, "BRL"),
		Categories:   []string{"travel", "hotels", "stayapi"},
		ProductType:  "hotel",
		LogoURL:      image,
		ImageURL:     image,
		Rating:       stayRatingText(item["rating"], item["star_rating"], item["hotel_class"], item["stars"]),
		ReviewScore:  stayFirstString(item["review_score"], item["score"]),
		Price:        stayPriceText(item["price"], item["rate"], item["nightly_rate"], item["price_per_night"], item["min_price"], item["rates"]),
		RoomType:     stayRoomText(item["room"], item["room_type"], item["room_name"], item["rooms"], item["rates"]),
		Available:    true,
		BookingLinks: stayLinks(item),
	}
}

func stayLinks(item map[string]any) map[string]any {
	if links, ok := item["links"].(map[string]any); ok {
		return links
	}
	out := map[string]any{}
	for _, key := range []string{"booking_url", "url", "official_website", "booking_com", "expedia", "hotels_com"} {
		if value := strings.TrimSpace(fmt.Sprint(item[key])); value != "" && value != "<nil>" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stayLocationText(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		text := firstNonEmptyStr(
			stayFirstString(typed["address"]),
			stayFirstString(typed["formatted_address"]),
			stayFirstString(typed["full_address"]),
			stayFirstString(typed["city"]),
			stayFirstString(typed["locality"]),
			stayFirstString(typed["region"]),
			stayFirstString(typed["state"]),
			stayFirstString(typed["country"]),
			stayFirstString(typed["name"]),
		)
		if text != "" {
			return text
		}
		if hasCoordinateKeys(typed) {
			return ""
		}
		return stayFirstString(value)
	default:
		text := stayFirstString(value)
		if looksLikeCoordinates(text) {
			return ""
		}
		return text
	}
}

func hasCoordinateKeys(value map[string]any) bool {
	_, hasLat := value["lat"]
	if !hasLat {
		_, hasLat = value["latitude"]
	}
	_, hasLng := value["lng"]
	if !hasLng {
		_, hasLng = value["longitude"]
	}
	if !hasLng {
		_, hasLng = value["lon"]
	}
	return hasLat && hasLng
}

func looksLikeCoordinates(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(lower, "lat") && (strings.Contains(lower, "lng") || strings.Contains(lower, "lon"))
}

func stayRatingText(values ...any) string {
	for _, value := range values {
		switch typed := value.(type) {
		case map[string]any:
			if text := stayFirstString(typed["value"], typed["rating"], typed["score"], typed["stars"]); text != "" {
				return text
			}
		default:
			if text := stayFirstString(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func stayPriceText(values ...any) string {
	for _, value := range values {
		switch typed := value.(type) {
		case map[string]any:
			if text := stayFirstString(typed["display"], typed["formatted"], typed["current"], typed["price_per_night"], typed["regular"], typed["value"], typed["amount"], typed["total"], typed["min"]); text != "" {
				return text
			}
		case []any:
			if len(typed) > 0 {
				if text := stayPriceText(typed[0]); text != "" {
					return text
				}
			}
		default:
			if text := stayFirstString(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func stayRoomText(values ...any) string {
	for _, value := range values {
		switch typed := value.(type) {
		case map[string]any:
			if text := stayFirstString(typed["name"], typed["title"], typed["room_name"], typed["type"], typed["description"]); text != "" {
				return text
			}
		case []any:
			if len(typed) > 0 {
				if text := stayRoomText(typed[0]); text != "" {
					return text
				}
			}
		default:
			if text := stayFirstString(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func stayFirstString(values ...any) string {
	for _, value := range values {
		switch typed := value.(type) {
		case []any:
			if len(typed) > 0 {
				if text := stayFirstString(typed[0]); text != "" {
					return text
				}
			}
		case map[string]any:
			if text := stayFirstString(
				typed["url"],
				typed["src"],
				typed["image_url"],
				typed["thumbnail_url"],
				typed["large"],
				typed["medium"],
				typed["small"],
				typed["original"],
				typed["href"],
			); text != "" {
				return text
			}
			if text := stayFirstString(typed["name"], typed["title"], typed["text"], typed["label"], typed["display_name"], typed["value"]); text != "" {
				return text
			}
		default:
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func stayAPIHTTPErrorCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "provider_unauthorized"
	case http.StatusForbidden:
		return "provider_forbidden"
	case http.StatusPaymentRequired:
		return "provider_quota_exhausted"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusTooManyRequests:
		return "provider_rate_limited"
	default:
		if status >= 500 {
			return "provider_unavailable"
		}
		return "provider_invalid_request"
	}
}

func stayAPISafeMessage(raw []byte) string {
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err == nil {
		for _, key := range []string{"message", "error"} {
			if text := strings.TrimSpace(fmt.Sprint(decoded[key])); text != "" && text != "<nil>" {
				if len(text) > 180 {
					return text[:180]
				}
				return text
			}
		}
	}
	return http.StatusText(http.StatusBadGateway)
}

func stayAPIErrorCode(err error) string {
	if providerErr, ok := err.(stayAPIError); ok {
		return providerErr.Code
	}
	return "provider_error"
}
