package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"payment-gateway/internal/agents"
	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/email"
	"payment-gateway/internal/paymaster"
	"payment-gateway/internal/psp"
	"payment-gateway/internal/webhooks"
	"payment-gateway/internal/workers"
)

type Server struct {
	cfg              *config.Config
	db               *database.DB
	workers          *workers.WorkerManager
	email            *email.Service
	limiter          rateLimitStore
	globalLimiter    rateLimitStore
	penaltyBox       *penaltyBox
	webhookRegistry  *webhooks.Registry
	webhooks         *webhooks.Dispatcher
	webhookLogs      *webhooks.Logs
	webhookDashboard *webhooks.Dashboard
	agents           *agents.Client
	paymaster        *paymaster.Service
	eipProbes        *eipProbeRunner
	requestLogQueue  chan database.APIRequestLogInput
	requestLogDrops  atomic.Int64
	discoveryCache   map[string]cachedDiscoveryDocument
	discoveryCacheMu sync.Mutex
	agentEpisodes    []agentEpisode
	agentEpisodesMu  sync.Mutex
	a2aTasks         map[string]*a2aTask
	a2aTasksMu       sync.Mutex
	pspRouter        *psp.Router // PSP abstraction; nil = legacy inline Efí parsing

	// Chaos / adversarial engine (optional — wired from main.go).
	adversarialEngine AdversarialEngine
	chaosMu           sync.Mutex
	chaosRunning      bool

	certReadyMu      sync.Mutex
	certReadyChecked time.Time
	certReadySource  string
	certReadyOK      bool
	certReadyErr     string

	efiMu                 sync.Mutex
	efiClient             *http.Client
	efiClientSource       string
	efiPixAccessToken     string
	efiPixAccessTokenExp  time.Time
	efiPixRefreshing      bool
	efiPixRefreshDone     chan struct{}
	efiBillAccessToken    string
	efiBillAccessTokenExp time.Time
	efiBillRefreshing     bool
	efiBillRefreshDone    chan struct{}

	readyMu      sync.Mutex
	readyChecked time.Time
	readyStatus  int
	readyPayload map[string]any
}

type requestIDContextKey struct{}

type buyFeeBreakdown struct {
	Tier           string  `json:"tier"`
	ServiceBps     int     `json:"serviceBps"`
	ServiceFee     float64 `json:"serviceFee"`
	NetworkFee     float64 `json:"networkFee"`
	MinFee         float64 `json:"minFee"`
	TotalFee       float64 `json:"totalFee"`
	RateSpreadBps  int     `json:"rateSpreadBps"`
	DisplayFee     float64 `json:"displayFee,omitempty"`
	EmbeddedSpread float64 `json:"embeddedSpread,omitempty"`
	TotalMargin    float64 `json:"totalMargin,omitempty"`
}

func New(cfg *config.Config, db *database.DB, workerMgr *workers.WorkerManager, mailer *email.Service) *Server {
	webhookDispatcher := webhooks.New(db, cfg)
	s := &Server{
		cfg:              cfg,
		db:               db,
		workers:          workerMgr,
		email:            mailer,
		limiter:          newConfiguredRateLimiter(cfg, cfg.OrderRateLimitWindowMs, cfg.OrderRateLimitMax),
		globalLimiter:    newConfiguredRateLimiter(cfg, cfg.RateLimitWindowMs, cfg.RateLimitMax),
		penaltyBox:       newPenaltyBoxFromConfig(cfg),
		webhookRegistry:  webhooks.NewRegistry(db),
		webhooks:         webhookDispatcher,
		webhookLogs:      webhooks.NewLogs(db),
		webhookDashboard: webhooks.NewDashboard(db),
		agents:           agents.NewClient(cfg),
		eipProbes:        newEIPProbeRunner(cfg, db),
		requestLogQueue:  make(chan database.APIRequestLogInput, 4096),
		discoveryCache:   make(map[string]cachedDiscoveryDocument),
		a2aTasks:         make(map[string]*a2aTask),
	}
	if db != nil {
		go s.runRequestLogWorker()
	}
	return s
}

// WithPaymaster attaches a paymaster.Service to the Server.
// Called from the main entrypoint after the RPC pool is initialised.
func (s *Server) WithPaymaster(svc *paymaster.Service) {
	s.paymaster = svc
}

// WithPSP attaches a PSP Router so PIX webhooks are normalised through the
// provider abstraction layer instead of using inline Efí JSON parsing.
// When router is nil the existing inline behaviour is preserved (backward-compat).
func (s *Server) WithPSP(router *psp.Router) {
	s.pspRouter = router
}

// WithAdversarialEngine attaches the chaos/adversarial engine used by
// POST /v1/admin/gas/chaos-run.  When nil the endpoint returns 503.
func (s *Server) WithAdversarialEngine(e AdversarialEngine) {
	s.adversarialEngine = e
}

func (s *Server) StartWebhooks(ctx context.Context) {
	if s == nil || s.webhooks == nil || s.workers == nil || s.workers.Bus == nil {
		return
	}
	s.webhooks.Start(ctx, s.workers.Bus)
}

func csvContains(csv, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, item := range strings.Split(csv, ",") {
		if strings.TrimSpace(item) == value {
			return true
		}
	}
	return false
}

func cloneJSONRequest(r *http.Request, payload any) *http.Request {
	raw, _ := json.Marshal(payload)
	clone := r.Clone(r.Context())
	clone.Body = io.NopCloser(bytes.NewReader(raw))
	clone.ContentLength = int64(len(raw))
	clone.Header = r.Header.Clone()
	clone.Header.Set("Content-Type", "application/json")
	return clone
}

func chainFXFakeWallet() string {
	return "0x000000000000000000000000000000000000dEaD"
}

func maskCSVKeys(csv string) []string {
	var out []string
	for _, item := range strings.Split(csv, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, maskAPIKey(item))
	}
	return out
}

func maskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 12 {
		return key
	}
	return key[:8] + "..." + key[len(key)-4:]
}
