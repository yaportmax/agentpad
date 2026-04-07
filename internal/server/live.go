package server

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"

	"github.com/cyrusaf/agentpad/internal/collab"
	"github.com/cyrusaf/agentpad/internal/domain"
	"github.com/cyrusaf/agentpad/internal/store"
)

type Selection struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type Presence struct {
	SessionID string     `json:"session_id"`
	Name      string     `json:"name"`
	Color     string     `json:"color"`
	Selection *Selection `json:"selection,omitempty"`
}

type clientMessage struct {
	Type      string     `json:"type"`
	Selection *Selection `json:"selection,omitempty"`
	Op        *collab.Op `json:"op,omitempty"`
}

type serverMessage struct {
	Type      string           `json:"type"`
	SessionID string           `json:"session_id,omitempty"`
	Document  *domain.Document `json:"document,omitempty"`
	Revision  int64            `json:"revision,omitempty"`
	Op        *collab.Op       `json:"op,omitempty"`
	Presence  []Presence       `json:"presence,omitempty"`
	Artifact  string           `json:"artifact,omitempty"`
	Data      map[string]any   `json:"data,omitempty"`
	Error     *domain.Error    `json:"error,omitempty"`
}

type client struct {
	id         string
	name       string
	color      string
	documentID string
	conn       *websocket.Conn
	hub        *Hub
	sendMu     sync.Mutex
}

type docRoom struct {
	clients  map[string]*client
	presence map[string]Presence
}

type Hub struct {
	store store.Store
	mu    sync.Mutex
	rooms map[string]*docRoom
}

func NewHub(s store.Store) *Hub {
	return &Hub{store: s, rooms: map[string]*docRoom{}}
}

func (h *Hub) HandleLive(w http.ResponseWriter, r *http.Request) {
	documentID := r.PathValue("id")
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "browser-user"
	}
	doc, err := h.store.GetDocument(r.Context(), documentID, name)
	if err != nil {
		writeError(w, err)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "session closed")

	client := &client{
		id:         uuid.NewString(),
		name:       name,
		color:      colorForName(name),
		documentID: documentID,
		conn:       conn,
		hub:        h,
	}
	h.addClient(documentID, client)
	defer h.removeClient(documentID, client.id)

	if err := client.send(serverMessage{
		Type:      "snapshot",
		SessionID: client.id,
		Document:  &doc,
		Presence:  h.snapshotPresence(documentID),
	}); err != nil {
		return
	}

	for {
		var msg clientMessage
		if err := wsjson.Read(r.Context(), conn, &msg); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure && websocket.CloseStatus(err) != websocket.StatusGoingAway {
				log.Printf("live read error: %v", err)
			}
			return
		}
		switch msg.Type {
		case "presence.update":
			h.updatePresence(documentID, client.id, msg.Selection)
		case "op.submit":
			if msg.Op == nil {
				_ = client.send(serverMessage{Type: "error", Error: domain.NewError(domain.ErrCodeInvalidRequest, "missing op", 400)})
				continue
			}
			msg.Op.Author = client.name
			doc, canonical, err := h.store.ApplyOp(r.Context(), documentID, *msg.Op, client.name)
			if err != nil {
				_ = client.send(serverMessage{Type: "error", Error: domain.AsError(err)})
				continue
			}
			_ = client.send(serverMessage{Type: "op.ack", Revision: doc.Revision, Op: &canonical})
			h.broadcast(documentID, client.id, serverMessage{Type: "op.applied", Revision: doc.Revision, Op: &canonical})
			h.NotifyDocument(documentID, "threads", map[string]any{"revision": doc.Revision})
		}
	}
}

func (h *Hub) NotifyDocument(documentID, artifact string, data map[string]any) {
	h.broadcast(documentID, "", serverMessage{
		Type:     "artifact.changed",
		Artifact: artifact,
		Data:     data,
	})
}

func (h *Hub) NotifyOpApplied(documentID string, revision int64, op collab.Op) {
	h.broadcast(documentID, "", serverMessage{
		Type:     "op.applied",
		Revision: revision,
		Op:       &op,
	})
}

func (h *Hub) addClient(documentID string, c *client) {
	h.mu.Lock()
	room, ok := h.rooms[documentID]
	if !ok {
		room = &docRoom{clients: map[string]*client{}, presence: map[string]Presence{}}
		h.rooms[documentID] = room
	}
	room.clients[c.id] = c
	room.presence[c.id] = Presence{SessionID: c.id, Name: c.name, Color: c.color}
	presence := snapshotPresenceLocked(room)
	h.mu.Unlock()
	h.broadcast(documentID, c.id, serverMessage{Type: "presence.changed", Presence: presence})
}

func (h *Hub) removeClient(documentID, clientID string) {
	h.mu.Lock()
	room, ok := h.rooms[documentID]
	if !ok {
		h.mu.Unlock()
		return
	}
	delete(room.clients, clientID)
	delete(room.presence, clientID)
	presence := snapshotPresenceLocked(room)
	if len(room.clients) == 0 {
		delete(h.rooms, documentID)
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	h.broadcast(documentID, "", serverMessage{Type: "presence.changed", Presence: presence})
}

func (h *Hub) updatePresence(documentID, clientID string, selection *Selection) {
	h.mu.Lock()
	room, ok := h.rooms[documentID]
	if !ok {
		h.mu.Unlock()
		return
	}
	p := room.presence[clientID]
	p.Selection = selection
	room.presence[clientID] = p
	presence := make([]Presence, 0, len(room.presence))
	for _, item := range room.presence {
		presence = append(presence, item)
	}
	h.mu.Unlock()
	h.broadcast(documentID, "", serverMessage{Type: "presence.changed", Presence: presence})
}

func (h *Hub) snapshotPresence(documentID string) []Presence {
	h.mu.Lock()
	defer h.mu.Unlock()
	room, ok := h.rooms[documentID]
	if !ok {
		return nil
	}
	return snapshotPresenceLocked(room)
}

func snapshotPresenceLocked(room *docRoom) []Presence {
	presence := make([]Presence, 0, len(room.presence))
	for _, item := range room.presence {
		presence = append(presence, item)
	}
	return presence
}

func (h *Hub) broadcast(documentID, skipClientID string, msg serverMessage) {
	h.mu.Lock()
	room, ok := h.rooms[documentID]
	if !ok {
		h.mu.Unlock()
		return
	}
	clients := make([]*client, 0, len(room.clients))
	for _, c := range room.clients {
		if c.id == skipClientID {
			continue
		}
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c.sendMu.Lock()
		err := wsjson.Write(ctx, c.conn, msg)
		c.sendMu.Unlock()
		cancel()
		if err != nil {
			log.Printf("live write error: %v", err)
		}
	}
}

func (c *client) send(msg serverMessage) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return wsjson.Write(ctx, c.conn, msg)
}

func colorForName(name string) string {
	palette := []string{"#1d4ed8", "#0f766e", "#b45309", "#7c3aed", "#be123c", "#0369a1"}
	sum := 0
	for _, r := range name {
		sum += int(r)
	}
	return palette[sum%len(palette)]
}
