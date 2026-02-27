package web

import (
	"log/slog"
	"sync"

	"github.com/gorilla/websocket"
)

// client represents a WebSocket connection subscribed to a task's output.
type client struct {
	taskID int64
	conn   *websocket.Conn
	send   chan string
}

// Hub manages WebSocket connections and broadcasts task output.
type Hub struct {
	mu      sync.RWMutex
	clients map[int64][]*client // taskID → clients
}

// NewHub creates a new Hub.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[int64][]*client),
	}
}

// Register adds a WebSocket connection for a task and starts the write pump.
func (h *Hub) Register(taskID int64, conn *websocket.Conn) {
	c := &client{
		taskID: taskID,
		conn:   conn,
		send:   make(chan string, 256),
	}

	h.mu.Lock()
	h.clients[taskID] = append(h.clients[taskID], c)
	h.mu.Unlock()

	// Start write pump — sends queued messages to the WebSocket.
	go func() {
		defer func() {
			h.unregister(c)
			conn.Close()
		}()
		for msg := range c.send {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
				slog.Debug("websocket write error", "err", err)
				return
			}
		}
	}()

	// Read pump — drains incoming messages (we don't use them, but need to read to detect close).
	go func() {
		defer func() {
			h.unregister(c)
			conn.Close()
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
}

// Broadcast sends a chunk to all clients subscribed to a task.
func (h *Hub) Broadcast(taskID int64, chunk string) {
	h.mu.RLock()
	clients := h.clients[taskID]
	h.mu.RUnlock()

	for _, c := range clients {
		select {
		case c.send <- chunk:
		default:
			// Client buffer full — drop message to avoid blocking.
			slog.Debug("websocket client buffer full, dropping chunk", "task_id", taskID)
		}
	}
}

// unregister removes a client and closes its send channel.
func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients := h.clients[c.taskID]
	for i, cl := range clients {
		if cl == c {
			h.clients[c.taskID] = append(clients[:i], clients[i+1:]...)
			close(c.send)
			break
		}
	}
	if len(h.clients[c.taskID]) == 0 {
		delete(h.clients, c.taskID)
	}
}
