package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cyrusaf/agentpad/internal/docmodel"
	"github.com/cyrusaf/agentpad/internal/domain"
	"github.com/cyrusaf/agentpad/internal/importexport"
)

func openTestStore(t *testing.T) (Store, string) {
	t.Helper()
	root := t.TempDir()
	st, err := Open(root)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	return st, root
}

func TestCreateDocumentPersistsBundle(t *testing.T) {
	ctx := context.Background()
	st, root := openTestStore(t)

	doc, err := st.CreateDocument(ctx, "Spec", domain.DocumentFormatMarkdown, "# Title\n\nHello world.\n", "tester")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	if _, err := filepath.Glob(filepath.Join(root, "documents", doc.ID+".json")); err != nil {
		t.Fatalf("glob bundle: %v", err)
	}

	reopenedStore, err := Open(root)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopenedStore.Close() })

	reopened, err := reopenedStore.GetDocument(ctx, doc.ID, "tester")
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	if reopened.ID != doc.ID || reopened.Title != "Spec" {
		t.Fatalf("unexpected reopened document: %+v", reopened)
	}
}

func TestImportDocumentCreatesBundleWithImportRecord(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)

	doc, err := st.ImportDocument(ctx, importexport.Imported{
		Title:        "Imported",
		Format:       domain.DocumentFormatMarkdown,
		Source:       "# Imported\n",
		SourcePath:   "notes.md",
		SourceFormat: domain.DocumentFormatMarkdown,
	}, "tester")
	if err != nil {
		t.Fatalf("import document: %v", err)
	}
	if doc.ID == "" {
		t.Fatalf("expected document id")
	}
}

func TestReadDocumentSupportsFullAndMatchReads(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)

	doc, err := st.CreateDocument(ctx, "Plan", domain.DocumentFormatMarkdown, "# Title\n\nAlpha plan.\n\nBeta plan.\n", "tester")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	full, err := st.ReadDocument(ctx, doc.ID, "tester", ReadOptions{})
	if err != nil {
		t.Fatalf("full read: %v", err)
	}
	if full.Scope != "full" || !strings.Contains(full.Text, "Alpha plan.") {
		t.Fatalf("unexpected full read: %+v", full)
	}

	match, err := st.ReadDocument(ctx, doc.ID, "tester", ReadOptions{
		Match:  "plan",
		Before: "Alpha ",
	})
	if err != nil {
		t.Fatalf("match read: %v", err)
	}
	if match.Scope != "match" || match.Text != "plan" {
		t.Fatalf("unexpected match read: %+v", match)
	}
}

func TestReadDocumentMatchFailsWhenAmbiguousOrMissing(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)

	doc, err := st.CreateDocument(ctx, "Plan", domain.DocumentFormatMarkdown, "# Title\n\nAlpha plan.\n\nBeta plan.\n", "tester")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	if _, err := st.ReadDocument(ctx, doc.ID, "tester", ReadOptions{Match: "plan"}); err == nil {
		t.Fatalf("expected ambiguous match error")
	} else if appErr := domain.AsError(err); appErr.Code != domain.ErrCodeConflict {
		t.Fatalf("expected conflict error, got %+v", appErr)
	}

	if _, err := st.ReadDocument(ctx, doc.ID, "tester", ReadOptions{Match: "missing"}); err == nil {
		t.Fatalf("expected missing match error")
	} else if appErr := domain.AsError(err); appErr.StatusCode != 404 {
		t.Fatalf("expected 404, got %+v", appErr)
	}
}

func TestGenericEditLeavesThreadUnresolvedWhenQuotedTextChanges(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)

	doc, err := st.CreateDocument(ctx, "Plan", domain.DocumentFormatMarkdown, "# Title\n\nHello world.\n", "tester")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 15, 20)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}
	if _, err := st.CreateThread(ctx, doc.ID, *anchor, "Keep this grounded", "reviewer"); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, _, err := st.ApplyMatchEdit(ctx, doc.ID, domain.MatchSelector{Match: "world"}, "team", "editor"); err != nil {
		t.Fatalf("apply match edit: %v", err)
	}

	threads, err := st.ListThreads(ctx, doc.ID, "tester")
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
	if len(threads) != 1 || threads[0].Anchor.Resolved {
		t.Fatalf("expected unresolved thread after generic match edit, got %+v", threads)
	}
}

func TestThreadEditRetargetsThreadToReplacement(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)

	doc, err := st.CreateDocument(ctx, "Plan", domain.DocumentFormatMarkdown, "# Title\n\nHello world.\n", "tester")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	thread, err := st.CreateThreadByMatch(ctx, doc.ID, domain.MatchSelector{Match: "world"}, "Replace this", "reviewer")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}

	updatedThread, updatedDoc, _, err := st.ApplyThreadEdit(ctx, doc.ID, thread.ID, "team", "editor")
	if err != nil {
		t.Fatalf("apply thread edit: %v", err)
	}
	if !strings.Contains(updatedDoc.Source, "Hello team.") {
		t.Fatalf("expected updated source, got %q", updatedDoc.Source)
	}
	if !updatedThread.Anchor.Resolved || updatedThread.Anchor.Quote != "team" {
		t.Fatalf("expected retargeted thread anchor, got %+v", updatedThread.Anchor)
	}
}
