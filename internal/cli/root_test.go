package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"net/http/httptest"

	"github.com/cyrusaf/agentpad/internal/domain"
	"github.com/cyrusaf/agentpad/internal/server"
	"github.com/cyrusaf/agentpad/internal/store"
)

func newTestServer(t *testing.T) string {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ts := httptest.NewServer(server.New(st, "").Routes())
	t.Cleanup(ts.Close)
	return ts.URL
}

func executeCommand(t *testing.T, serverURL string, args ...string) string {
	t.Helper()
	cmd := NewRootCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs(append([]string{"--server", serverURL, "--name", "Tester"}, args...))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute %v: %v\n%s", args, err, stdout.String())
	}
	return stdout.String()
}

func TestDocsCreateJSON(t *testing.T) {
	serverURL := newTestServer(t)
	output := executeCommand(t, serverURL, "--json", "docs", "create", "--title", "Spec", "--text", "# Title\n\nHello")

	var result OpenResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if result.DocumentID == "" || !strings.Contains(result.URL, "/"+result.DocumentID) {
		t.Fatalf("unexpected create output: %+v", result)
	}
}

func TestDocsImportJSON(t *testing.T) {
	serverURL := newTestServer(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("# Imported\n"), 0o644); err != nil {
		t.Fatalf("write import file: %v", err)
	}

	output := executeCommand(t, serverURL, "--json", "docs", "import", path)
	var result OpenResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if result.DocumentID == "" || result.Title != "notes" {
		t.Fatalf("unexpected import output: %+v", result)
	}
}

func TestDocsReadAndEdit(t *testing.T) {
	serverURL := newTestServer(t)
	createOut := executeCommand(t, serverURL, "--json", "docs", "create", "--title", "Spec", "--text", "# Title\n\nHello world")
	var created OpenResult
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("decode create output: %v", err)
	}

	readOut := executeCommand(t, serverURL, "docs", "read", created.DocumentID, "--match", "world")
	if strings.TrimSpace(readOut) != "world" {
		t.Fatalf("unexpected read output: %q", readOut)
	}

	_ = executeCommand(t, serverURL, "edit", created.DocumentID, "--match", "world", "--replace", "team")
	fullRead := executeCommand(t, serverURL, "docs", "read", created.DocumentID)
	if !strings.Contains(fullRead, "Hello team") {
		t.Fatalf("expected edited document, got %q", fullRead)
	}
}

func TestDocsReadWritesOutputFile(t *testing.T) {
	serverURL := newTestServer(t)
	createOut := executeCommand(t, serverURL, "--json", "docs", "create", "--title", "Spec", "--text", "# Title\n\nHello world")
	var created OpenResult
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("decode create output: %v", err)
	}

	outputPath := filepath.Join(t.TempDir(), "doc.txt")
	writtenPath := strings.TrimSpace(executeCommand(t, serverURL, "docs", "read", created.DocumentID, "--output", outputPath))
	if writtenPath != outputPath {
		t.Fatalf("expected output path %q, got %q", outputPath, writtenPath)
	}
	body, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if !strings.Contains(string(body), "Hello world") {
		t.Fatalf("unexpected file contents: %q", string(body))
	}
}

func TestThreadsCommandsUseDocumentIDs(t *testing.T) {
	serverURL := newTestServer(t)
	createOut := executeCommand(t, serverURL, "--json", "docs", "create", "--title", "Spec", "--text", "# Title\n\nHello world")
	var created OpenResult
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("decode create output: %v", err)
	}

	threadOut := executeCommand(t, serverURL, "--json", "threads", "create", created.DocumentID, "--match", "world", "--body", "Please revise this.")
	var thread domain.Thread
	if err := json.Unmarshal([]byte(threadOut), &thread); err != nil {
		t.Fatalf("decode thread: %v", err)
	}
	if thread.ID == "" {
		t.Fatalf("expected thread id")
	}

	_ = executeCommand(t, serverURL, "threads", "reply", created.DocumentID, thread.ID, "--body", "Agreed")
	resolveOut := executeCommand(t, serverURL, "--json", "threads", "resolve", created.DocumentID, thread.ID)
	var resolved domain.Thread
	if err := json.Unmarshal([]byte(resolveOut), &resolved); err != nil {
		t.Fatalf("decode resolved thread: %v", err)
	}
	if resolved.Status != domain.ThreadStatusResolved {
		t.Fatalf("expected resolved status, got %+v", resolved)
	}
}

func TestDocsOpenUsesPathnameURL(t *testing.T) {
	serverURL := newTestServer(t)
	createOut := executeCommand(t, serverURL, "--json", "docs", "create", "--title", "Spec", "--text", "# Title\n\nHello world")
	var created OpenResult
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("decode create output: %v", err)
	}

	previousBrowserOpener := browserOpener
	defer func() { browserOpener = previousBrowserOpener }()
	var openedURL string
	browserOpener = func(rawURL string) error {
		openedURL = rawURL
		return nil
	}

	_ = executeCommand(t, serverURL, "docs", "open", created.DocumentID)
	if !strings.HasSuffix(openedURL, "/"+created.DocumentID) {
		t.Fatalf("expected pathname url, got %q", openedURL)
	}
}
