package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
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
	Provider     string         `json:"provider"`
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
	values.Set("currency", firstNonEmptyStr(q.Currency, "BRL"))
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
	Currency string
}

func (s *Server) handleTravelHotels(w http.ResponseWriter, r *http.Request) {
	if !stayAPIEnabled() {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "provider": "stayapi", "configured": false})
		return
	}
	query := stayHotelQuery{
		Location: strings.TrimSpace(r.URL.Query().Get("location")),
		Search:   firstNonEmptyStr(strings.TrimSpace(r.URL.Query().Get("search")), strings.TrimSpace(r.URL.Query().Get("q"))),
		CheckIn:  strings.TrimSpace(r.URL.Query().Get("check_in")),
		CheckOut: strings.TrimSpace(r.URL.Query().Get("check_out")),
		Adults:   strings.TrimSpace(r.URL.Query().Get("adults")),
		Currency: strings.TrimSpace(r.URL.Query().Get("currency")),
	}
	hotels, err := newStayAPIClient().SearchHotels(r.Context(), query)
	if err != nil {
		status := http.StatusBadGateway
		if providerErr, ok := err.(stayAPIError); ok && providerErr.HTTPStatus > 0 && providerErr.HTTPStatus < 500 {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]any{"error": "erro ao consultar StayAPI", "code": stayAPIErrorCode(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": hotels, "provider": "stayapi", "configured": true})
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
		if len(out) >= 12 {
			break
		}
	}
	return out
}

func stayHotelItems(decoded map[string]any) []map[string]any {
	candidates := []any{decoded["data"], decoded["hotels"], decoded["results"], decoded["properties"]}
	if data, ok := decoded["data"].(map[string]any); ok {
		candidates = append(candidates, data["hotels"], data["results"], data["properties"], data["items"])
		if links, ok := data["links"].(map[string]any); ok {
			return []map[string]any{dataWithLinks(data, links)}
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
	return stayHotelProduct{
		ID:           "stayapi_" + id,
		Provider:     "stayapi",
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
