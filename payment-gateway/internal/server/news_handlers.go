package server

import (
	"net/http"

	"payment-gateway/internal/news"
)

// newsManager is initialised once at startup; its background goroutine
// refreshes RSS feeds every 3 minutes and caches the result.
var newsManager = news.New()

func handlePublicNews(w http.ResponseWriter, r *http.Request) {
	newsManager.Handle(w, r)
}
