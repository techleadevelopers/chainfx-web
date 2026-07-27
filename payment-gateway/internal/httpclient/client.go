package httpclient

import (
	"net/http"
	"sync"
	"time"
)

var (
	defaultOnce   sync.Once
	defaultClient *http.Client
)

// Default returns the shared HTTP client used by outbound workers.
func Default() *http.Client {
	defaultOnce.Do(func() {
		defaultClient = &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   16,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		}
	})
	return defaultClient
}
