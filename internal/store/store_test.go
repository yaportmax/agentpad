package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cyrusaf/agentpad/internal/collab"
	"github.com/cyrusaf/agentpad/internal/docmodel"
	"github.com/cyrusaf/agentpad/internal/domain"
)

func openTestStore(t *testing.T) (*Store, string) {
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

func writeTestDocument(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write document: %v", err)
	}
	return path
}

func TestStoreCentralMetadataAndSuggestions(t *testing.T) {
	ctx := context.Background()
	st, root := openTestStore(t)
	docPath := writeTestDocument(t, t.TempDir(), "spec.md", "# Title\n\nHello world.\n")

	doc, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}

	anchor, err := docmodel.AnchorFromSelection(doc, 15, 20)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}

	thread, err := st.CreateThread(ctx, doc.ID, *anchor, "Needs polish", "reviewer")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if thread.ID == "" || len(thread.Comments) != 1 {
		t.Fatalf("unexpected thread payload: %+v", thread)
	}

	if _, _, err := st.ApplyOp(ctx, doc.ID, collab.Op{
		Position:     15,
		DeleteCount:  0,
		InsertText:   "brave ",
		BaseRevision: doc.Revision,
	}, "editor"); err != nil {
		t.Fatalf("apply op: %v", err)
	}

	threads, err := st.ListThreads(ctx, doc.ID, "tester")
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
	if len(threads) != 1 || !threads[0].Anchor.Resolved {
		t.Fatalf("expected resolved anchor after nearby edit: %+v", threads)
	}

	annotation, err := st.CreateAnnotation(ctx, doc.ID, domain.Annotation{
		Kind:   "risk",
		Body:   "Watch this phrase",
		Anchor: anchor,
	}, "reviewer")
	if err != nil {
		t.Fatalf("create annotation: %v", err)
	}
	if annotation.ID == "" {
		t.Fatalf("annotation id was empty")
	}

	suggestion, err := st.CreateSuggestionBatch(ctx, doc.ID, domain.SuggestionBatch{
		BaseRevision: 1,
		Rationale:    "Improve tone",
		Ops: []domain.SuggestionOp{{
			Anchor:      *anchor,
			Replacement: "team",
		}},
	}, "ai")
	if err != nil {
		t.Fatalf("create suggestion batch: %v", err)
	}
	if suggestion.Status != domain.SuggestionStatusPending {
		t.Fatalf("expected pending suggestion, got %s", suggestion.Status)
	}

	applied, updatedDoc, err := st.ApplySuggestionBatch(ctx, doc.ID, suggestion.ID, "editor")
	if err != nil {
		t.Fatalf("apply suggestion: %v", err)
	}
	if applied.Status != domain.SuggestionStatusAccepted {
		t.Fatalf("expected accepted suggestion, got %s", applied.Status)
	}
	if !strings.Contains(updatedDoc.Source, "team") {
		t.Fatalf("expected updated document source, got %q", updatedDoc.Source)
	}

	events, err := st.Activity(ctx, doc.ID)
	if err != nil {
		t.Fatalf("activity: %v", err)
	}
	if len(events) < 4 {
		t.Fatalf("expected activity events, got %d", len(events))
	}

	for _, legacyPath := range []string{
		docPath + ".agentpad.json",
		docPath + ".agentpad.activity.ndjson",
		docPath + ".agentpad.ops.ndjson",
	} {
		if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected no sibling sidecar at %s", legacyPath)
		}
	}

	if _, err := os.Stat(filepath.Join(root, "index.json")); err != nil {
		t.Fatalf("expected central index: %v", err)
	}
	for _, pattern := range []string{
		filepath.Join(root, "files", "*", "meta.json"),
		filepath.Join(root, "files", "*", "activity.ndjson"),
		filepath.Join(root, "files", "*", "ops.ndjson"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		if len(matches) != 1 {
			t.Fatalf("expected one match for %s, got %d", pattern, len(matches))
		}
	}
}

func TestReadDocumentReturnsAnchorsForRangeBlockAndQuote(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)
	docPath := writeTestDocument(t, t.TempDir(), "plan.md", "# Title\n\nAlpha plan.\n\nBeta plan.\n")

	doc, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	var alphaBlock domain.Block
	for _, block := range doc.Blocks {
		if strings.Contains(block.Text, "Alpha plan.") {
			alphaBlock = block
			break
		}
	}
	if alphaBlock.ID == "" {
		t.Fatalf("expected alpha paragraph block")
	}

	rangeRead, err := st.ReadDocument(ctx, doc.ID, "tester", ReadOptions{Start: 9, End: 14})
	if err != nil {
		t.Fatalf("read range: %v", err)
	}
	if rangeRead.Anchor == nil || rangeRead.Anchor.DocStart != 9 || rangeRead.Anchor.DocEnd != 14 {
		t.Fatalf("expected range anchor, got %+v", rangeRead.Anchor)
	}

	blockRead, err := st.ReadDocument(ctx, doc.ID, "tester", ReadOptions{BlockID: alphaBlock.ID})
	if err != nil {
		t.Fatalf("read block: %v", err)
	}
	if blockRead.Anchor == nil || blockRead.Anchor.BlockID != alphaBlock.ID {
		t.Fatalf("expected block anchor, got %+v", blockRead.Anchor)
	}

	quoteRead, err := st.ReadDocument(ctx, doc.ID, "tester", ReadOptions{Quote: "plan", Prefix: "Alpha "})
	if err != nil {
		t.Fatalf("read quote: %v", err)
	}
	if quoteRead.Anchor == nil || quoteRead.Anchor.Quote != "plan" || quoteRead.Anchor.DocStart != 15 {
		t.Fatalf("expected quote anchor, got %+v", quoteRead.Anchor)
	}
}

func TestReadDocumentQuoteFailsWhenAmbiguousOrMissing(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)
	docPath := writeTestDocument(t, t.TempDir(), "plan.md", "# Title\n\nAlpha plan.\n\nBeta plan.\n")

	doc, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}

	if _, err := st.ReadDocument(ctx, doc.ID, "tester", ReadOptions{Quote: "plan"}); err == nil {
		t.Fatalf("expected ambiguous quote error")
	} else if appErr := domain.AsError(err); appErr.Code != domain.ErrCodeConflict {
		t.Fatalf("expected conflict error, got %+v", appErr)
	}

	if _, err := st.ReadDocument(ctx, doc.ID, "tester", ReadOptions{Quote: "missing"}); err == nil {
		t.Fatalf("expected missing quote error")
	} else if appErr := domain.AsError(err); appErr.StatusCode != 404 {
		t.Fatalf("expected 404, got %+v", appErr)
	}
}

func TestApplyAnchorEditRebasesAfterConcurrentInsert(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)
	docPath := writeTestDocument(t, t.TempDir(), "plan.md", "# Title\n\nHello world.\n")

	doc, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 15, 20)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}
	if _, _, err := st.ApplyOp(ctx, doc.ID, collab.Op{
		Position:     15,
		DeleteCount:  0,
		InsertText:   "brave ",
		BaseRevision: doc.Revision,
	}, "editor"); err != nil {
		t.Fatalf("apply op: %v", err)
	}

	updated, _, err := st.ApplyAnchorEdit(ctx, doc.ID, *anchor, "team", "editor")
	if err != nil {
		t.Fatalf("apply anchor edit: %v", err)
	}
	if !strings.Contains(updated.Source, "Hello brave team.") {
		t.Fatalf("expected rebased anchor edit, got %q", updated.Source)
	}
}

func TestApplyAnchorEditReturnsStaleAnchorError(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)
	docPath := writeTestDocument(t, t.TempDir(), "plan.md", "# Title\n\nHello world.\n")

	doc, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 15, 20)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}
	if _, _, err := st.ApplyOp(ctx, doc.ID, collab.Op{
		Position:     15,
		DeleteCount:  5,
		InsertText:   "earth",
		BaseRevision: doc.Revision,
	}, "editor"); err != nil {
		t.Fatalf("apply op: %v", err)
	}

	if _, _, err := st.ApplyAnchorEdit(ctx, doc.ID, *anchor, "team", "editor"); err == nil {
		t.Fatalf("expected stale anchor error")
	} else if appErr := domain.AsError(err); appErr.Code != domain.ErrCodeInvalidAnchor || appErr.StatusCode != 409 {
		t.Fatalf("expected stale invalid anchor error, got %+v", appErr)
	}
}

func TestListThreadsMarksAnchorUnresolvedAfterQuotedTextIsReplaced(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)
	docPath := writeTestDocument(t, t.TempDir(), "plan.md", "# Title\n\nHello world.\n")

	doc, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 15, 20)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}
	if _, err := st.CreateThread(ctx, doc.ID, *anchor, "Keep this grounded", "reviewer"); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, _, err := st.ApplyOp(ctx, doc.ID, collab.Op{
		Position:     15,
		DeleteCount:  5,
		InsertText:   "team",
		BaseRevision: doc.Revision,
	}, "editor"); err != nil {
		t.Fatalf("apply op: %v", err)
	}

	threads, err := st.ListThreads(ctx, doc.ID, "tester")
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected one thread, got %d", len(threads))
	}
	if threads[0].Anchor.Resolved {
		t.Fatalf("expected thread anchor to be unresolved after replacement, got %+v", threads[0].Anchor)
	}
	if threads[0].Anchor.Quote != "world" {
		t.Fatalf("expected original quote to be preserved, got %+v", threads[0].Anchor)
	}
}

func TestApplyThreadEditRetargetsThreadToReplacementText(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)
	docPath := writeTestDocument(t, t.TempDir(), "plan.md", "# Title\n\nHello world.\n")

	doc, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 15, 20)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}
	thread, err := st.CreateThread(ctx, doc.ID, *anchor, "Replace this", "reviewer")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}

	updatedThread, updatedDoc, op, err := st.ApplyThreadEdit(ctx, doc.ID, thread.ID, "team", "editor")
	if err != nil {
		t.Fatalf("apply thread edit: %v", err)
	}
	if !strings.Contains(updatedDoc.Source, "Hello team.") {
		t.Fatalf("expected updated document source, got %q", updatedDoc.Source)
	}
	if op.Position != 15 || op.DeleteCount != 5 {
		t.Fatalf("expected canonical replace op, got %+v", op)
	}
	if !updatedThread.Anchor.Resolved || updatedThread.Anchor.Quote != "team" {
		t.Fatalf("expected thread anchor to retarget to replacement text, got %+v", updatedThread.Anchor)
	}

	refetched, err := st.GetThread(ctx, doc.ID, thread.ID, "tester")
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if refetched.Anchor.Quote != "team" || !refetched.Anchor.Resolved {
		t.Fatalf("expected persisted retargeted anchor, got %+v", refetched.Anchor)
	}
}

func TestReanchorThreadPersistsRecoveredAnchor(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)
	docPath := writeTestDocument(t, t.TempDir(), "plan.md", "# Title\n\nHello world.\n")

	doc, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 15, 20)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}
	thread, err := st.CreateThread(ctx, doc.ID, *anchor, "Keep this grounded", "reviewer")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, _, err := st.ApplyAnchorEdit(ctx, doc.ID, *anchor, "team", "editor"); err != nil {
		t.Fatalf("apply anchor edit: %v", err)
	}

	updatedDoc, err := st.GetDocument(ctx, doc.ID, "tester")
	if err != nil {
		t.Fatalf("get updated document: %v", err)
	}
	reanchor, err := docmodel.AnchorFromSelection(updatedDoc, 15, 19)
	if err != nil {
		t.Fatalf("select replacement span: %v", err)
	}

	updatedThread, err := st.ReanchorThread(ctx, doc.ID, thread.ID, *reanchor, "editor")
	if err != nil {
		t.Fatalf("reanchor thread: %v", err)
	}
	if updatedThread.Anchor.Quote != "team" || !updatedThread.Anchor.Resolved {
		t.Fatalf("expected reanchored thread to point at replacement, got %+v", updatedThread.Anchor)
	}

	refetched, err := st.GetThread(ctx, doc.ID, thread.ID, "tester")
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if refetched.Anchor.Quote != "team" || !refetched.Anchor.Resolved {
		t.Fatalf("expected persisted reanchored thread, got %+v", refetched.Anchor)
	}
}

func TestReanchorThreadRejectsInvalidReplacementAnchor(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)
	docPath := writeTestDocument(t, t.TempDir(), "plan.md", "# Title\n\nHello world.\n")

	doc, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 15, 20)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}
	thread, err := st.CreateThread(ctx, doc.ID, *anchor, "Keep this grounded", "reviewer")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, _, err := st.ApplyAnchorEdit(ctx, doc.ID, *anchor, "team", "editor"); err != nil {
		t.Fatalf("apply anchor edit: %v", err)
	}

	if _, err := st.ReanchorThread(ctx, doc.ID, thread.ID, *anchor, "editor"); err == nil {
		t.Fatalf("expected invalid reanchor error")
	} else if appErr := domain.AsError(err); appErr.Code != domain.ErrCodeInvalidAnchor {
		t.Fatalf("expected invalid anchor error, got %+v", appErr)
	}
}

func TestBlockIDsStableAcrossReopen(t *testing.T) {
	ctx := context.Background()
	st, root := openTestStore(t)
	docPath := writeTestDocument(t, t.TempDir(), "plan.md", "# Title\n\nAlpha plan.\n\nBeta plan.\n")

	doc, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	initialIDs := make([]string, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
		initialIDs = append(initialIDs, block.ID)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	st2, err := Open(root)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	reopened, err := st2.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("reopen document: %v", err)
	}
	if len(reopened.Blocks) != len(initialIDs) {
		t.Fatalf("expected same number of blocks, got %d vs %d", len(reopened.Blocks), len(initialIDs))
	}
	for index, block := range reopened.Blocks {
		if block.ID != initialIDs[index] {
			t.Fatalf("expected stable block ids, got %q vs %q", block.ID, initialIDs[index])
		}
	}
}

func TestStorePersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	docPath := writeTestDocument(t, t.TempDir(), "persist.md", "# Title\n\nHello world.\n")

	st, err := Open(root)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	doc, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 9, 14)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}
	thread, err := st.CreateThread(ctx, doc.ID, *anchor, "Persist me", "reviewer")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	st2, err := Open(root)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	doc2, err := st2.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("reopen document: %v", err)
	}
	threads, err := st2.ListThreads(ctx, doc2.ID, "tester")
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
	if len(threads) != 1 || threads[0].ID != thread.ID {
		t.Fatalf("expected persisted thread, got %+v", threads)
	}
}

func TestStoreRenameRelinksByIdentity(t *testing.T) {
	ctx := context.Background()
	st, _ := openTestStore(t)
	dir := t.TempDir()
	docPath := writeTestDocument(t, dir, "rename.md", "# Title\n\nHello world.\n")

	_, info, err := validateDocumentPath(docPath)
	if err != nil {
		t.Fatalf("validate document: %v", err)
	}
	if !fileIdentityFromInfo(info).Available {
		t.Skip("filesystem identity unavailable on this platform")
	}

	doc, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 9, 14)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}
	if _, err := st.CreateThread(ctx, doc.ID, *anchor, "Keep me on rename", "reviewer"); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	renamedPath := filepath.Join(dir, "renamed.md")
	if err := os.Rename(docPath, renamedPath); err != nil {
		t.Fatalf("rename document: %v", err)
	}

	renamedDoc, err := st.OpenDocument(ctx, renamedPath, "tester")
	if err != nil {
		t.Fatalf("open renamed document: %v", err)
	}
	canonicalRenamedPath, _, err := validateDocumentPath(renamedPath)
	if err != nil {
		t.Fatalf("validate renamed document: %v", err)
	}
	if renamedDoc.ID != canonicalRenamedPath {
		t.Fatalf("expected renamed path id %s, got %s", canonicalRenamedPath, renamedDoc.ID)
	}
	threads, err := st.ListThreads(ctx, renamedDoc.ID, "tester")
	if err != nil {
		t.Fatalf("list renamed threads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected one preserved thread, got %d", len(threads))
	}
}

func TestStoreFallsBackToPathWhenIdentityChanges(t *testing.T) {
	ctx := context.Background()
	st, root := openTestStore(t)
	dir := t.TempDir()
	docPath := writeTestDocument(t, dir, "replace.md", "# Title\n\nHello world.\n")

	doc, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 9, 14)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}
	if _, err := st.CreateThread(ctx, doc.ID, *anchor, "Keep me on replace", "reviewer"); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	backupPath := filepath.Join(dir, "replace-backup.md")
	if err := os.Rename(docPath, backupPath); err != nil {
		t.Fatalf("backup original file: %v", err)
	}
	if err := os.WriteFile(docPath, []byte("# Title\n\nReplacement body.\n"), 0o644); err != nil {
		t.Fatalf("write replacement document: %v", err)
	}

	doc2, err := st.OpenDocument(ctx, docPath, "tester")
	if err != nil {
		t.Fatalf("open replacement document: %v", err)
	}
	threads, err := st.ListThreads(ctx, doc2.ID, "tester")
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected metadata to stay attached by path fallback, got %d threads", len(threads))
	}

	matches, err := filepath.Glob(filepath.Join(root, "files", "*", "meta.json"))
	if err != nil {
		t.Fatalf("glob meta files: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one metadata record after replacement, got %d", len(matches))
	}
}

func TestStoreCopyCreatesNewMetadataRecord(t *testing.T) {
	ctx := context.Background()
	st, root := openTestStore(t)
	dir := t.TempDir()
	originalPath := writeTestDocument(t, dir, "original.md", "# Title\n\nHello world.\n")

	doc, err := st.OpenDocument(ctx, originalPath, "tester")
	if err != nil {
		t.Fatalf("open original document: %v", err)
	}
	anchor, err := docmodel.AnchorFromSelection(doc, 9, 14)
	if err != nil {
		t.Fatalf("anchor selection: %v", err)
	}
	if _, err := st.CreateThread(ctx, doc.ID, *anchor, "Original thread", "reviewer"); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	copyPath := writeTestDocument(t, dir, "copy.md", doc.Source)
	copyDoc, err := st.OpenDocument(ctx, copyPath, "tester")
	if err != nil {
		t.Fatalf("open copied document: %v", err)
	}
	threads, err := st.ListThreads(ctx, copyDoc.ID, "tester")
	if err != nil {
		t.Fatalf("list copied threads: %v", err)
	}
	if len(threads) != 0 {
		t.Fatalf("expected copied file to start fresh, got %d threads", len(threads))
	}

	matches, err := filepath.Glob(filepath.Join(root, "files", "*", "meta.json"))
	if err != nil {
		t.Fatalf("glob meta files: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected two metadata records after copy, got %d", len(matches))
	}
}
