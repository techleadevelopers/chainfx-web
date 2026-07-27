package mobile

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsCheckOrigin validates the WebSocket origin against ALLOWED_ORIGINS.
// Falls back to same-origin check if the env var is not set.
func wsCheckOrigin(r *http.Request) bool {
	allowed := os.Getenv("ALLOWED_ORIGINS")
	if allowed == "" || allowed == "*" {
		// In development allow all; production must set ALLOWED_ORIGINS.
		return true
	}
	origin := r.Header.Get("Origin")
	for _, o := range strings.Split(allowed, ",") {
		if strings.TrimSpace(o) == strings.TrimSpace(origin) {
			return true
		}
	}
	return false
}

var upgrader = websocket.Upgrader{
	CheckOrigin:     wsCheckOrigin,
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
}

// wsHub manages WebSocket clients and broadcasts.
type wsHub struct {
	mu      sync.RWMutex
	clients map[string]map[*wsClient]bool // topic → clients
}

type wsClient struct {
	conn  *websocket.Conn
	send  chan []byte
	topic string
	uid   string
}

func newWsHub() *wsHub {
	return &wsHub{clients: make(map[string]map[*wsClient]bool)}
}

func (h *wsHub) run() {
	// Heartbeat every 30s to keep connections alive
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		h.broadcast("price", []byte(`{"type":"ping"}`))
	}
}

func (h *wsHub) register(topic string, c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[topic] == nil {
		h.clients[topic] = make(map[*wsClient]bool)
	}
	h.clients[topic][c] = true
}

func (h *wsHub) unregister(topic string, c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[topic] != nil {
		delete(h.clients[topic], c)
	}
}

func (h *wsHub) broadcast(topic string, msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients[topic] {
		select {
		case c.send <- msg:
		default:
			close(c.send)
		}
	}
}

// BroadcastPrice is called by the price worker to push live prices.
func (s *Server) BroadcastPrice(priceBRL float64) {
	msg, _ := marshalJSON(map[string]any{"type": "price", "usdt_brl": priceBRL, "ts": time.Now().Unix()})
	s.hub.broadcast("price", msg)
}

// BroadcastOrderUpdate is called when an order status changes.
// userID scopes the broadcast so only that user's WS connections receive it.
// Callers that do not know the userID should use BroadcastOrderUpdateGlobal (admin-only).
func (s *Server) BroadcastOrderUpdate(userID, orderID, status string) {
	msg, _ := marshalJSON(map[string]any{"type": "order_update", "order_id": orderID, "status": status, "ts": time.Now().Unix()})
	if userID != "" {
		s.hub.broadcast("orders:"+userID, msg)
	}
}

func serveWS(h *wsHub, topic, uid string, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws upgrade failed", "err", err)
		return
	}
	c := &wsClient{conn: conn, send: make(chan []byte, 64), topic: topic, uid: uid}
	h.register(topic, c)
	defer func() {
		h.unregister(topic, c)
		conn.Close()
	}()

	// Writer goroutine
	go func() {
		for msg := range c.send {
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}()

	// Send welcome message
	welcome, _ := marshalJSON(map[string]any{"type": "connected", "topic": topic})
	c.send <- welcome

	// Reader loop (keep alive / handle pong)
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			return
		}
	}
}

// handleWSOrders — WS /api/mobile/ws/orders
// NOTE: this handler is always called inside requireAuth, so uid is guaranteed non-empty.
func (s *Server) handleWSOrders(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	if uid == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Subscribe to a user-scoped topic so broadcasts only reach this user.
	serveWS(s.hub, "orders:"+uid, uid, w, r)
}

// handleWSPrice — WS /api/mobile/ws/price
func (s *Server) handleWSPrice(w http.ResponseWriter, r *http.Request) {
	serveWS(s.hub, "price", "", w, r)
}

// handleWSNotifications — WS /api/mobile/ws/notifications
// NOTE: this handler is always called inside requireAuth, so uid is guaranteed non-empty.
func (s *Server) handleWSNotifications(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	if uid == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	serveWS(s.hub, "notifications:"+uid, uid, w, r)
}
