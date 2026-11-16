// Package ws implements the document-events WebSocket channel. The browser
// connects here to learn when a document changes (design: docs/backend-api.md,
// section "WebSocket"). Events are hints: the client refetches state on receipt
// and resyncs on reconnect, so a missed event can never leave it stale.
package ws

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	maxMessageSize = 4096             // client -> server messages are small
	writeWait      = 10 * time.Second // time allowed to write a message
	pongWait       = 60 * time.Second // wait for a pong before dropping
	pingPeriod     = 25 * time.Second // server ping cadence
	sendBuffer     = 32               // outbound events per connection
	maxSubs        = 20               // max subscriptions per connection
)

// Event is an outgoing JSON envelope: {"v":1,"type":..., ...}.
type Event map[string]any

func newEvent(typ string) Event {
	return Event{"v": 1, "type": typ}
}

// UpdatingPDF reports that a script change was made and a render is pending
// (debounced). The UI shows a loading state.
func UpdatingPDF(docID string) Event {
	e := newEvent("updating_pdf")
	e["doc_id"] = docID
	return e
}

// PDFUpdated reports a committed + rendered version; the UI refetches the PDF
// and manifest from pdfURL / manifestURL.
func PDFUpdated(docID string, version int, pdfURL, manifestURL string) Event {
	e := newEvent("pdf_updated")
	e["doc_id"] = docID
	e["version"] = version
	e["actor"] = "system"
	e["cover_url"] = fmt.Sprintf("/docs/%s/cover?version=%d", docID, version)
	if pdfURL != "" {
		e["pdf_url"] = pdfURL
	}
	if manifestURL != "" {
		e["manifest_url"] = manifestURL
	}
	return e
}

// PDFFailed reports a failed render; the current PDF is unchanged.
func PDFFailed(docID string, version int, err string) Event {
	e := newEvent("pdf_failed")
	e["doc_id"] = docID
	e["version"] = version
	e["error"] = err
	return e
}

// Options supplies the service-level callbacks the handler needs.
type Options struct {
	// CanAccess authorizes a subscription. Dev default: allow all.
	CanAccess func(userID, docID string) bool
	// CurrentVersion returns the head version of a document, if it exists.
	CurrentVersion func(docID string) (int, bool)
}

// Hub fans out events to subscribed connections, per document.
type Hub struct {
	mu   sync.RWMutex
	docs map[string]map[*Client]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{docs: map[string]map[*Client]struct{}{}}
}

func (h *Hub) subscribe(docID string, c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.docs[docID] == nil {
		h.docs[docID] = map[*Client]struct{}{}
	}
	h.docs[docID][c] = struct{}{}
}

func (h *Hub) unsubscribe(docID string, c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m, ok := h.docs[docID]; ok {
		delete(m, c)
		if len(m) == 0 {
			delete(h.docs, docID)
		}
	}
}

func (h *Hub) removeClient(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for docID, m := range h.docs {
		if _, ok := m[c]; ok {
			delete(m, c)
			if len(m) == 0 {
				delete(h.docs, docID)
			}
		}
	}
}

// Publish sends event to every connection subscribed to docID. It never
// blocks: a slow consumer is dropped instead of stalling the publisher.
func (h *Hub) Publish(docID string, event Event) {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.docs[docID]))
	for c := range h.docs[docID] {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		c.enqueue(event)
	}
}

// clientMsg is the client -> server envelope.
type clientMsg struct {
	V     int    `json:"v"`
	Type  string `json:"type"`
	DocID string `json:"doc_id"`
}

// Client is one WebSocket connection.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte

	userID    string
	canAccess func(userID, docID string) bool
	curVer    func(docID string) (int, bool)

	mu   sync.Mutex
	subs map[string]bool

	closeOnce sync.Once
	done      chan struct{}
}

// enqueue queues an event for the writer. On a full buffer the connection is
// closed rather than blocking the publisher; the client resyncs on reconnect.
func (c *Client) enqueue(e Event) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	case <-c.done:
	default:
		c.close()
	}
}

func (c *Client) enqueueError(code, message, docID string) {
	e := newEvent("error")
	e["code"] = code
	e["message"] = message
	if docID != "" {
		e["doc_id"] = docID
	}
	c.enqueue(e)
}

func (c *Client) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		c.hub.removeClient(c)
		_ = c.conn.Close()
	})
}

func (c *Client) handle(msg clientMsg) {
	switch msg.Type {
	case "subscribe":
		if msg.DocID == "" {
			c.enqueueError("bad_request", "doc_id required", "")
			return
		}
		if c.canAccess != nil && !c.canAccess(c.userID, msg.DocID) {
			c.enqueueError("forbidden", "access denied", msg.DocID)
			return
		}
		c.mu.Lock()
		if len(c.subs) >= maxSubs {
			c.mu.Unlock()
			c.enqueueError("limit", "too many subscriptions", msg.DocID)
			return
		}
		c.subs[msg.DocID] = true
		c.mu.Unlock()

		c.hub.subscribe(msg.DocID, c)
		version := 0
		if c.curVer != nil {
			if v, ok := c.curVer(msg.DocID); ok {
				version = v
			}
		}
		e := newEvent("subscribed")
		e["doc_id"] = msg.DocID
		e["version"] = version
		c.enqueue(e)

	case "unsubscribe":
		c.mu.Lock()
		delete(c.subs, msg.DocID)
		c.mu.Unlock()
		c.hub.unsubscribe(msg.DocID, c)

	case "ping":
		c.enqueue(newEvent("pong"))

	default:
		// Unknown type: ignore (forward compatibility).
	}
}

func (c *Client) readPump() {
	defer c.close()
	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var msg clientMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		c.handle(msg)
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case data := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				c.close()
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.close()
				return
			}
		case <-c.done:
			return
		}
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Dev: allow any origin. Production restricts the Origin allow-list and
	// uses ticket auth (design sections 4.1 and 8).
	CheckOrigin: func(*http.Request) bool { return true },
}

// Handler upgrades a connection and serves it until it disconnects.
func (h *Hub) Handler(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := &Client{
			hub:       h,
			conn:      conn,
			send:      make(chan []byte, sendBuffer),
			canAccess: opts.CanAccess,
			curVer:    opts.CurrentVersion,
			subs:      map[string]bool{},
			done:      make(chan struct{}),
		}
		log.Printf("ws connect")
		go c.writePump()
		c.readPump() // blocks until the client disconnects
		log.Printf("ws disconnect")
	}
}
