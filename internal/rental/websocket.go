package rental

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// AllowedWSOrigins contains origins permitted for WebSocket connections
var AllowedWSOrigins = map[string]bool{
	"https://hashforge.bch2.org": true,
	"https://bch2.org":           true,
	"https://www.bch2.org":       true,
	"http://localhost:8081":      true, // Development only
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Same-origin requests may not have Origin header
			return true
		}
		return AllowedWSOrigins[origin]
	},
}

// WSMessage represents a WebSocket message
type WSMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// OrderUpdate represents an order status update
type OrderUpdate struct {
	OrderID     int     `json:"order_id"`
	Status      string  `json:"status"`
	SpentBTC    string  `json:"spent_btc"`
	RefundBTC   string  `json:"refund_btc,omitempty"`
	HashrateAvg float64 `json:"hashrate_avg,omitempty"`
	ElapsedSec  int     `json:"elapsed_sec,omitempty"`
}

// BalanceUpdate represents a balance update
type BalanceUpdate struct {
	BalanceBTC    string `json:"balance_btc"`
	AvailableBTC  string `json:"available_btc"`
	PendingBTC    string `json:"pending_btc"`
}

// WSHub manages WebSocket connections per customer
type WSHub struct {
	clients    map[int]map[*WSClient]bool // customerID -> clients
	broadcast  chan *CustomerMessage
	register   chan *WSClient
	unregister chan *WSClient
	mu         sync.RWMutex
}

// CustomerMessage is a message targeted to a specific customer
type CustomerMessage struct {
	CustomerID int
	Message    *WSMessage
}

// WSClient represents a WebSocket client connection
type WSClient struct {
	hub        *WSHub
	conn       *websocket.Conn
	customerID int
	send       chan []byte
}

// NewWSHub creates a new WebSocket hub
func NewWSHub() *WSHub {
	return &WSHub{
		clients:    make(map[int]map[*WSClient]bool),
		broadcast:  make(chan *CustomerMessage, 256),
		register:   make(chan *WSClient),
		unregister: make(chan *WSClient),
	}
}

// Run starts the hub's main loop
func (h *WSHub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			if h.clients[client.customerID] == nil {
				h.clients[client.customerID] = make(map[*WSClient]bool)
			}
			h.clients[client.customerID][client] = true
			h.mu.Unlock()

		case client := <-h.unregister:
			h.mu.Lock()
			if clients, ok := h.clients[client.customerID]; ok {
				if _, ok := clients[client]; ok {
					delete(clients, client)
					close(client.send)
					if len(clients) == 0 {
						delete(h.clients, client.customerID)
					}
				}
			}
			h.mu.Unlock()

		case msg := <-h.broadcast:
			h.mu.RLock()
			if clients, ok := h.clients[msg.CustomerID]; ok {
				data, err := json.Marshal(msg.Message)
				if err != nil {
					h.mu.RUnlock()
					continue
				}
				for client := range clients {
					select {
					case client.send <- data:
					default:
						// Client buffer full, skip
					}
				}
			}
			h.mu.RUnlock()
		}
	}
}

// SendToCustomer sends a message to all connections for a customer
func (h *WSHub) SendToCustomer(customerID int, msg *WSMessage) {
	h.broadcast <- &CustomerMessage{
		CustomerID: customerID,
		Message:    msg,
	}
}

// SendOrderUpdate sends an order update to a customer
func (h *WSHub) SendOrderUpdate(customerID int, update *OrderUpdate) {
	h.SendToCustomer(customerID, &WSMessage{
		Type:    "order_update",
		Payload: update,
	})
}

// SendBalanceUpdate sends a balance update to a customer
func (h *WSHub) SendBalanceUpdate(customerID int, update *BalanceUpdate) {
	h.SendToCustomer(customerID, &WSMessage{
		Type:    "balance_update",
		Payload: update,
	})
}

// writePump pumps messages from the hub to the WebSocket connection
func (c *WSClient) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// readPump pumps messages from the WebSocket connection to the hub
func (c *WSClient) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(512)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("websocket error: %v", err)
			}
			break
		}
	}
}

// HandleWebSocket handles WebSocket upgrade requests
func (h *WebHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade error: %v", err)
		return
	}

	client := &WSClient{
		hub:        h.wsHub,
		conn:       conn,
		customerID: customer.ID,
		send:       make(chan []byte, 256),
	}

	h.wsHub.register <- client

	go client.writePump()
	go client.readPump()
}
