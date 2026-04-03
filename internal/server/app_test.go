package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cyrusaf/agentpad/internal/docmodel"
	"github.com/cyrusaf/agentpad/internal/domain"
	"github.com/cyrusaf/agentpad/internal/store"
)

func TestThreadEndpointsCreateAndReply(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	docPath := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(docPath, []byte("# Title\n\nHello world"), 0o644); err != nil {
		t.Fatalf("write document: %v", err)
	}
	doc, err := st.OpenDocument(context.Background(), docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}

	app := New(st, "")
	server := httptest.NewServer(app.Routes())
	t.Cleanup(server.Close)

	threadPayload := map[string]any{"path": doc.ID, "start": 9, "end": 20, "body": "Needs tightening"}
	resp, err := postJSON(server.URL+"/api/files/threads", threadPayload)
	if err != nil {
		t.Fatalf("thread request failed: %v", err)
	}
	defer resp.Body.Close()
	var thread domain.Thread
	if err := json.NewDecoder(resp.Body).Decode(&thread); err != nil {
		t.Fatalf("decode thread: %v", err)
	}
	if thread.ID == "" {
		t.Fatalf("expected thread ID")
	}

	replyPayload := map[string]any{"path": doc.ID, "thread_id": thread.ID, "body": "Agreed"}
	if _, err := postJSON(server.URL+"/api/files/thread-replies", replyPayload); err != nil {
		t.Fatalf("reply request failed: %v", err)
	}

	threads, err := st.ListThreads(context.Background(), doc.ID, "tester")
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected one thread, got %d", len(threads))
	}
	if len(threads[0].Comments) != 2 {
		t.Fatalf("expected two comments, got %d", len(threads[0].Comments))
	}
}

func TestEmptyArtifactEndpointsReturnArrays(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	docPath := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(docPath, []byte("# Title\n\nHello world"), 0o644); err != nil {
		t.Fatalf("write document: %v", err)
	}
	doc, err := st.OpenDocument(context.Background(), docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}

	app := New(st, "")
	server := httptest.NewServer(app.Routes())
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL + "/api/files/threads?path=" + url.QueryEscape(doc.ID))
	if err != nil {
		t.Fatalf("get threads: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read threads: %v", err)
	}
	if strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("expected [] from threads, got %s", body)
	}
}

func TestEditEndpointAppliesCollaborativeOp(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	docPath := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(docPath, []byte("# Title\n\nHello world"), 0o644); err != nil {
		t.Fatalf("write document: %v", err)
	}
	doc, err := st.OpenDocument(context.Background(), docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}

	app := New(st, "")
	server := httptest.NewServer(app.Routes())
	t.Cleanup(server.Close)

	editPayload := map[string]any{
		"path":          doc.ID,
		"position":      9,
		"delete_count":  5,
		"insert_text":   "Team",
		"base_revision": doc.Revision,
	}
	resp, err := postJSON(server.URL+"/api/files/edit", editPayload)
	if err != nil {
		t.Fatalf("edit request failed: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Document domain.Document `json:"document"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode edit response: %v", err)
	}
	if !strings.Contains(result.Document.Source, "Team world") {
		t.Fatalf("expected updated source in response, got %q", result.Document.Source)
	}

	body, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if !strings.Contains(string(body), "Team world") {
		t.Fatalf("expected on-disk document update, got %q", string(body))
	}
}

func TestEditEndpointAppliesAnchorEdit(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	docPath := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(docPath, []byte("# Title\n\nHello world"), 0o644); err != nil {
		t.Fatalf("write document: %v", err)
	}
	doc, err := st.OpenDocument(context.Background(), docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 9, 14)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}

	app := New(st, "")
	server := httptest.NewServer(app.Routes())
	t.Cleanup(server.Close)

	editPayload := map[string]any{
		"path":        doc.ID,
		"insert_text": "Team",
		"anchor":      anchor,
	}
	resp, err := postJSON(server.URL+"/api/files/edit", editPayload)
	if err != nil {
		t.Fatalf("anchor edit request failed: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Document domain.Document `json:"document"`
		Op       map[string]any  `json:"op"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode anchor edit response: %v", err)
	}
	if !strings.Contains(result.Document.Source, "Team world") {
		t.Fatalf("expected updated source in response, got %q", result.Document.Source)
	}
	if _, ok := result.Op["position"]; !ok {
		t.Fatalf("expected canonical op in response, got %+v", result.Op)
	}
}

func TestOpenEndpointSupportsSummaryMode(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	docPath := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(docPath, []byte("# Title\n\nHello world"), 0o644); err != nil {
		t.Fatalf("write document: %v", err)
	}
	doc, err := st.OpenDocument(context.Background(), docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}

	app := New(st, "")
	server := httptest.NewServer(app.Routes())
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL + "/api/files/open?path=" + url.QueryEscape(doc.ID) + "&full=false")
	if err != nil {
		t.Fatalf("open summary request failed: %v", err)
	}
	defer resp.Body.Close()

	var summary domain.DocumentSummary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary.ID != doc.ID || summary.Title != doc.Title || summary.Revision != doc.Revision {
		t.Fatalf("unexpected summary payload: %+v", summary)
	}
}

func TestEditEndpointSupportsThreadAwareEdit(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	docPath := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(docPath, []byte("# Title\n\nHello world"), 0o644); err != nil {
		t.Fatalf("write document: %v", err)
	}
	doc, err := st.OpenDocument(context.Background(), docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 9, 14)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}
	thread, err := st.CreateThread(context.Background(), doc.ID, *anchor, "Replace this", "tester")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}

	app := New(st, "")
	server := httptest.NewServer(app.Routes())
	t.Cleanup(server.Close)

	editPayload := map[string]any{
		"path":        doc.ID,
		"thread_id":   thread.ID,
		"insert_text": "Team",
	}
	resp, err := postJSON(server.URL+"/api/files/edit", editPayload)
	if err != nil {
		t.Fatalf("thread edit request failed: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Document domain.Document `json:"document"`
		Thread   domain.Thread   `json:"thread"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode thread edit response: %v", err)
	}
	if !strings.Contains(result.Document.Source, "Team world") {
		t.Fatalf("expected updated source in response, got %q", result.Document.Source)
	}
	if result.Thread.Anchor.Quote != "Team" || !result.Thread.Anchor.Resolved {
		t.Fatalf("expected thread anchor to retarget, got %+v", result.Thread.Anchor)
	}
}

func TestThreadReanchorEndpointReanchorsUnresolvedThread(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	docPath := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(docPath, []byte("# Title\n\nHello world"), 0o644); err != nil {
		t.Fatalf("write document: %v", err)
	}
	doc, err := st.OpenDocument(context.Background(), docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 9, 14)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}
	thread, err := st.CreateThread(context.Background(), doc.ID, *anchor, "Keep this grounded", "tester")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, _, err := st.ApplyAnchorEdit(context.Background(), doc.ID, *anchor, "Team", "tester"); err != nil {
		t.Fatalf("apply anchor edit: %v", err)
	}

	app := New(st, "")
	server := httptest.NewServer(app.Routes())
	t.Cleanup(server.Close)

	reanchorPayload := map[string]any{
		"path":      doc.ID,
		"thread_id": thread.ID,
		"start":     9,
		"end":       13,
	}
	resp, err := postJSON(server.URL+"/api/files/thread-reanchor", reanchorPayload)
	if err != nil {
		t.Fatalf("thread reanchor request failed: %v", err)
	}
	defer resp.Body.Close()

	var result domain.Thread
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode thread reanchor response: %v", err)
	}
	if result.Anchor.Quote != "Team" || !result.Anchor.Resolved {
		t.Fatalf("expected reanchored thread, got %+v", result.Anchor)
	}
}

func postJSON(url string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AgentPad-Actor", "tester")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp, nil
}
