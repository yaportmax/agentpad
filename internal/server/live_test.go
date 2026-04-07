package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/cyrusaf/agentpad/internal/domain"
	"github.com/cyrusaf/agentpad/internal/store"
)

func TestLiveSessionSendsSnapshot(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	doc, err := st.CreateDocument(context.Background(), "Live", domain.DocumentFormatMarkdown, "# Hello\n\nLive world", "tester")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	app := New(st, "")
	ts := httptest.NewServer(app.Routes())
	t.Cleanup(ts.Close)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/documents/" + doc.ID + "/live?name=tester"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test complete")

	var msg serverMessage
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if msg.Type != "snapshot" || msg.Document == nil || msg.Document.ID != doc.ID {
		t.Fatalf("unexpected snapshot: %+v", msg)
	}
}

func TestHTTPDocumentEditsBroadcastAppliedOps(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	doc, err := st.CreateDocument(context.Background(), "Live edit", domain.DocumentFormatMarkdown, "# Hello\n\nLive world", "tester")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	app := New(st, "")
	ts := httptest.NewServer(app.Routes())
	t.Cleanup(ts.Close)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/documents/" + doc.ID + "/live?name=browser-user"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test complete")

	var snapshot serverMessage
	if err := wsjson.Read(ctx, conn, &snapshot); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if snapshot.Type != "snapshot" {
		t.Fatalf("expected snapshot, got %s", snapshot.Type)
	}

	payload, err := json.Marshal(map[string]any{
		"match":   "Live",
		"replace": "Team",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/documents/"+doc.ID+"/edit", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AgentPad-Actor", "cli-user")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post edit: %v", err)
	}
	resp.Body.Close()

	var msg serverMessage
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatalf("read live op: %v", err)
	}
	if msg.Type != "op.applied" || msg.Op == nil {
		t.Fatalf("unexpected op message: %+v", msg)
	}
	if msg.Op.Author != "cli-user" || msg.Op.InsertText != "Team" {
		t.Fatalf("unexpected op payload: %+v", msg.Op)
	}
}
