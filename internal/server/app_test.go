package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"testing/fstest"

	"github.com/cyrusaf/agentpad/internal/domain"
	"github.com/cyrusaf/agentpad/internal/store"
)

func TestCreateAndFetchDocumentEndpoints(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	app := New(st, "")
	ts := httptest.NewServer(app.Routes())
	t.Cleanup(ts.Close)

	resp, err := postJSON(ts.URL+"/api/documents", map[string]any{
		"title":  "Spec",
		"format": "markdown",
		"source": "# Title\n\nHello world",
	})
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	defer resp.Body.Close()

	var created domain.Document
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("expected document id")
	}

	summaryResp, err := http.Get(ts.URL + "/api/documents/" + created.ID + "?summary=true")
	if err != nil {
		t.Fatalf("get summary: %v", err)
	}
	defer summaryResp.Body.Close()

	var summary domain.DocumentSummary
	if err := json.NewDecoder(summaryResp.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary.ID != created.ID || summary.Title != "Spec" {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestImportDocumentEndpoint(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	app := New(st, "")
	ts := httptest.NewServer(app.Routes())
	t.Cleanup(ts.Close)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "notes.md")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("# Imported\n")); err != nil {
		t.Fatalf("write import body: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/documents/import", &body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload import: %v", err)
	}
	defer resp.Body.Close()

	var doc domain.Document
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode imported document: %v", err)
	}
	if doc.ID == "" || doc.Title != "notes" {
		t.Fatalf("unexpected imported document: %+v", doc)
	}
}

func TestEditAndThreadEndpoints(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	doc, err := st.CreateDocument(context.Background(), "Doc", domain.DocumentFormatMarkdown, "# Title\n\nHello world", "tester")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	app := New(st, "")
	ts := httptest.NewServer(app.Routes())
	t.Cleanup(ts.Close)

	editResp, err := postJSON(ts.URL+"/api/documents/"+doc.ID+"/edit", map[string]any{
		"match":   "world",
		"replace": "team",
	})
	if err != nil {
		t.Fatalf("edit request failed: %v", err)
	}
	defer editResp.Body.Close()

	var editResult struct {
		Document domain.Document `json:"document"`
	}
	if err := json.NewDecoder(editResp.Body).Decode(&editResult); err != nil {
		t.Fatalf("decode edit response: %v", err)
	}
	if editResult.Document.Source == doc.Source || editResult.Document.Revision != doc.Revision+1 {
		t.Fatalf("unexpected edit result: %+v", editResult.Document)
	}

	threadResp, err := postJSON(ts.URL+"/api/documents/"+doc.ID+"/threads", map[string]any{
		"body":  "Please revise this.",
		"match": "team",
	})
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	defer threadResp.Body.Close()

	var thread domain.Thread
	if err := json.NewDecoder(threadResp.Body).Decode(&thread); err != nil {
		t.Fatalf("decode thread: %v", err)
	}
	if thread.ID == "" {
		t.Fatalf("expected thread id")
	}

	replyResp, err := postJSON(ts.URL+"/api/documents/"+doc.ID+"/threads/"+url.PathEscape(thread.ID)+"/replies", map[string]any{
		"body": "Agreed",
	})
	if err != nil {
		t.Fatalf("reply thread: %v", err)
	}
	replyResp.Body.Close()

	resolveResp, err := postJSON(ts.URL+"/api/documents/"+doc.ID+"/threads/"+url.PathEscape(thread.ID)+"/resolve", map[string]any{})
	if err != nil {
		t.Fatalf("resolve thread: %v", err)
	}
	defer resolveResp.Body.Close()

	var resolved domain.Thread
	if err := json.NewDecoder(resolveResp.Body).Decode(&resolved); err != nil {
		t.Fatalf("decode resolved thread: %v", err)
	}
	if resolved.Status != domain.ThreadStatusResolved {
		t.Fatalf("expected resolved status, got %+v", resolved)
	}
}

func TestEmbeddedStaticFSServesAppAndFallback(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	app := NewWithStaticFS(st, fstest.MapFS{
		"index.html":    {Data: []byte("<html>AgentPad</html>")},
		"assets/app.js": {Data: []byte("console.log('agentpad')")},
	})
	ts := httptest.NewServer(app.Routes())
	t.Cleanup(ts.Close)

	for _, path := range []string{"/", "/documents/example"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if resp.StatusCode != http.StatusOK || string(body) != "<html>AgentPad</html>" {
			t.Fatalf("unexpected index response for %s: status=%d body=%q", path, resp.StatusCode, body)
		}
	}

	resp, err := http.Get(ts.URL + "/assets/app.js")
	if err != nil {
		t.Fatalf("get asset: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read asset: %v", err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "console.log('agentpad')" {
		t.Fatalf("unexpected asset response: status=%d body=%q", resp.StatusCode, body)
	}
}

func postJSON(rawURL string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(req)
}
