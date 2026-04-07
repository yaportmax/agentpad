package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cyrusaf/agentpad/internal/collab"
	"github.com/cyrusaf/agentpad/internal/docmodel"
	"github.com/cyrusaf/agentpad/internal/domain"
	"github.com/cyrusaf/agentpad/internal/importexport"
)

const bundleVersion = 1

type Store interface {
	Close() error
	CreateDocument(ctx context.Context, title string, format domain.DocumentFormat, source, actor string) (domain.Document, error)
	ImportDocument(ctx context.Context, imported importexport.Imported, actor string) (domain.Document, error)
	GetDocument(ctx context.Context, documentID, actor string) (domain.Document, error)
	ReadDocument(ctx context.Context, documentID, actor string, opts ReadOptions) (domain.DocumentRead, error)
	ChangesSince(ctx context.Context, documentID string, revision int64) ([]collab.Op, error)
	ApplyOp(ctx context.Context, documentID string, op collab.Op, actor string) (domain.Document, collab.Op, error)
	ApplyAnchorEdit(ctx context.Context, documentID string, anchor domain.Anchor, replacement, actor string) (domain.Document, collab.Op, error)
	ApplyMatchEdit(ctx context.Context, documentID string, selector domain.MatchSelector, replacement, actor string) (domain.Document, collab.Op, error)
	ApplyThreadEdit(ctx context.Context, documentID, threadID, replacement, actor string) (domain.Thread, domain.Document, collab.Op, error)
	CreateThread(ctx context.Context, documentID string, anchor domain.Anchor, body, actor string) (domain.Thread, error)
	CreateThreadByMatch(ctx context.Context, documentID string, selector domain.MatchSelector, body, actor string) (domain.Thread, error)
	ListThreads(ctx context.Context, documentID string, actor string) ([]domain.Thread, error)
	GetThread(ctx context.Context, documentID, threadID, actor string) (domain.Thread, error)
	ReplyThread(ctx context.Context, documentID, threadID, body, actor string) (domain.Thread, domain.Comment, error)
	ReanchorThread(ctx context.Context, documentID, threadID string, anchor domain.Anchor, actor string) (domain.Thread, error)
	ReanchorThreadByMatch(ctx context.Context, documentID, threadID string, selector domain.MatchSelector, actor string) (domain.Thread, error)
	SetThreadStatus(ctx context.Context, documentID, threadID string, status domain.ThreadStatus, actor string) (domain.Thread, error)
}

type ReadOptions struct {
	Full       bool
	Match      string
	Before     string
	After      string
	Occurrence int
}

type LocalStorage struct {
	root string

	mu       sync.Mutex
	docLocks map[string]*sync.Mutex
	history  map[string][]collab.Op
}

type documentBundle struct {
	Version     int                      `json:"version"`
	Document    domain.Document          `json:"document"`
	Threads     []domain.Thread          `json:"threads"`
	Annotations []domain.Annotation      `json:"annotations"`
	Suggestions []domain.SuggestionBatch `json:"suggestions"`
	Imports     []domain.ImportRecord    `json:"imports"`
}

func Open(root string) (Store, error) {
	resolvedRoot, err := resolveStorageRoot(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(resolvedRoot, "documents"), 0o755); err != nil {
		return nil, err
	}
	return &LocalStorage{
		root:     resolvedRoot,
		docLocks: map[string]*sync.Mutex{},
		history:  map[string][]collab.Op{},
	}, nil
}

func (s *LocalStorage) Close() error { return nil }

func (s *LocalStorage) CreateDocument(ctx context.Context, title string, format domain.DocumentFormat, source, actor string) (domain.Document, error) {
	_ = ctx
	now := time.Now().UTC()
	source = normalizeSource(source)
	if strings.TrimSpace(title) == "" {
		title = "Untitled"
	}
	if format == "" {
		format = domain.DocumentFormatMarkdown
	}
	documentID := uuid.NewString()
	doc := domain.Document{
		ID:         documentID,
		Title:      title,
		Format:     format,
		Source:     source,
		Revision:   0,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastEdited: actor,
	}
	doc.Blocks = docmodel.BuildBlocks(doc.Format, doc.Source, nil)
	bundle := documentBundle{
		Version:     bundleVersion,
		Document:    doc,
		Threads:     []domain.Thread{},
		Annotations: []domain.Annotation{},
		Suggestions: []domain.SuggestionBatch{},
		Imports:     []domain.ImportRecord{},
	}
	if err := s.writeBundle(bundle); err != nil {
		return domain.Document{}, err
	}
	return doc, nil
}

func (s *LocalStorage) ImportDocument(ctx context.Context, imported importexport.Imported, actor string) (domain.Document, error) {
	doc, err := s.CreateDocument(ctx, imported.Title, imported.Format, imported.Source, actor)
	if err != nil {
		return domain.Document{}, err
	}
	unlock := s.lockDocument(doc.ID)
	defer unlock()
	bundle, err := s.loadBundle(doc.ID)
	if err != nil {
		return domain.Document{}, err
	}
	bundle.Imports = append(bundle.Imports, domain.ImportRecord{
		ID:           uuid.NewString(),
		DocumentID:   doc.ID,
		SourcePath:   imported.SourcePath,
		SourceFormat: imported.SourceFormat,
		Warnings:     imported.Warnings,
		CreatedAt:    time.Now().UTC(),
	})
	if err := s.writeBundle(bundle); err != nil {
		return domain.Document{}, err
	}
	return bundle.Document, nil
}

func (s *LocalStorage) GetDocument(ctx context.Context, documentID, actor string) (domain.Document, error) {
	_ = ctx
	_ = actor
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return domain.Document{}, err
	}
	return bundle.Document, nil
}

func (s *LocalStorage) ReadDocument(ctx context.Context, documentID, actor string, opts ReadOptions) (domain.DocumentRead, error) {
	doc, err := s.GetDocument(ctx, documentID, actor)
	if err != nil {
		return domain.DocumentRead{}, err
	}
	result := domain.DocumentRead{
		DocumentID: documentID,
		Revision:   doc.Revision,
		Scope:      "full",
		Text:       doc.Source,
	}
	if opts.Full {
		result.Blocks = doc.Blocks
	}
	if strings.TrimSpace(opts.Match) == "" {
		return result, nil
	}

	selector := domain.MatchSelector{
		Match:      opts.Match,
		Before:     opts.Before,
		After:      opts.After,
		Occurrence: opts.Occurrence,
	}
	anchor, err := resolveMatchSelector(doc, selector)
	if err != nil {
		return domain.DocumentRead{}, err
	}
	result.Scope = "match"
	result.Text = anchor.Quote
	result.Selector = &selector
	if opts.Full {
		result.Blocks = relevantBlocks(doc.Blocks, anchor.DocStart, anchor.DocEnd)
	}
	return result, nil
}

func (s *LocalStorage) ChangesSince(ctx context.Context, documentID string, revision int64) ([]collab.Op, error) {
	_ = ctx
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return nil, err
	}
	return s.changesSinceLocked(bundle.Document.ID, bundle.Document.Revision, revision)
}

func (s *LocalStorage) ApplyOp(ctx context.Context, documentID string, op collab.Op, actor string) (domain.Document, collab.Op, error) {
	_ = ctx
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	doc, canonical, err := s.applyOpLocked(bundle, op, actor)
	if err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	return doc, canonical, nil
}

func (s *LocalStorage) ApplyAnchorEdit(ctx context.Context, documentID string, anchor domain.Anchor, replacement, actor string) (domain.Document, collab.Op, error) {
	_ = ctx
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	resolved, err := s.resolveAnchorStrictLocked(bundle.Document, anchor)
	if err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	return s.applyOpLocked(bundle, collab.Op{
		Position:     resolved.DocStart,
		DeleteCount:  resolved.DocEnd - resolved.DocStart,
		InsertText:   replacement,
		BaseRevision: bundle.Document.Revision,
		Author:       actor,
	}, actor)
}

func (s *LocalStorage) ApplyMatchEdit(ctx context.Context, documentID string, selector domain.MatchSelector, replacement, actor string) (domain.Document, collab.Op, error) {
	_ = ctx
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	anchor, err := resolveMatchSelector(bundle.Document, selector)
	if err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	return s.applyOpLocked(bundle, collab.Op{
		Position:     anchor.DocStart,
		DeleteCount:  anchor.DocEnd - anchor.DocStart,
		InsertText:   replacement,
		BaseRevision: bundle.Document.Revision,
		Author:       actor,
	}, actor)
}

func (s *LocalStorage) ApplyThreadEdit(ctx context.Context, documentID, threadID, replacement, actor string) (domain.Thread, domain.Document, collab.Op, error) {
	_ = ctx
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return domain.Thread{}, domain.Document{}, collab.Op{}, err
	}
	index := threadIndex(bundle.Threads, threadID)
	if index < 0 {
		return domain.Thread{}, domain.Document{}, collab.Op{}, domain.NewError(domain.ErrCodeDocumentNotFound, "thread not found", 404)
	}
	resolved, err := s.resolveAnchorStrictLocked(bundle.Document, bundle.Threads[index].Anchor)
	if err != nil {
		return domain.Thread{}, domain.Document{}, collab.Op{}, err
	}
	updatedDoc, canonical, err := s.applyOpLocked(bundle, collab.Op{
		Position:     resolved.DocStart,
		DeleteCount:  resolved.DocEnd - resolved.DocStart,
		InsertText:   replacement,
		BaseRevision: bundle.Document.Revision,
		Author:       actor,
	}, actor)
	if err != nil {
		return domain.Thread{}, domain.Document{}, collab.Op{}, err
	}
	refreshed, err := s.loadBundle(documentID)
	if err != nil {
		return domain.Thread{}, domain.Document{}, collab.Op{}, err
	}
	index = threadIndex(refreshed.Threads, threadID)
	if index < 0 {
		return domain.Thread{}, domain.Document{}, collab.Op{}, domain.NewError(domain.ErrCodeDocumentNotFound, "thread not found", 404)
	}
	anchor, err := docmodel.AnchorFromSelection(updatedDoc, canonical.Position, canonical.Position+collab.RuneLen(replacement))
	if err != nil {
		return domain.Thread{}, domain.Document{}, collab.Op{}, err
	}
	refreshed.Threads[index].Anchor = *anchor
	refreshed.Threads[index].UpdatedAt = time.Now().UTC()
	if err := s.writeBundle(refreshed); err != nil {
		return domain.Thread{}, domain.Document{}, collab.Op{}, err
	}
	return refreshed.Threads[index], updatedDoc, canonical, nil
}

func (s *LocalStorage) CreateThread(ctx context.Context, documentID string, anchor domain.Anchor, body, actor string) (domain.Thread, error) {
	_ = ctx
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return domain.Thread{}, err
	}
	resolved, err := s.resolveAnchorStrictLocked(bundle.Document, anchor)
	if err != nil {
		return domain.Thread{}, err
	}
	now := time.Now().UTC()
	thread := domain.Thread{
		ID:         uuid.NewString(),
		DocumentID: documentID,
		Anchor:     resolved,
		Status:     domain.ThreadStatusOpen,
		Author:     actor,
		CreatedAt:  now,
		UpdatedAt:  now,
		Comments: []domain.Comment{{
			ID:        uuid.NewString(),
			ThreadID:  "",
			Author:    actor,
			Body:      body,
			CreatedAt: now,
		}},
	}
	thread.Comments[0].ThreadID = thread.ID
	bundle.Threads = append(bundle.Threads, thread)
	if err := s.writeBundle(bundle); err != nil {
		return domain.Thread{}, err
	}
	return thread, nil
}

func (s *LocalStorage) CreateThreadByMatch(ctx context.Context, documentID string, selector domain.MatchSelector, body, actor string) (domain.Thread, error) {
	_ = ctx
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return domain.Thread{}, err
	}
	anchor, err := resolveMatchSelector(bundle.Document, selector)
	if err != nil {
		return domain.Thread{}, err
	}
	now := time.Now().UTC()
	thread := domain.Thread{
		ID:         uuid.NewString(),
		DocumentID: documentID,
		Anchor:     *anchor,
		Status:     domain.ThreadStatusOpen,
		Author:     actor,
		CreatedAt:  now,
		UpdatedAt:  now,
		Comments: []domain.Comment{{
			ID:        uuid.NewString(),
			ThreadID:  "",
			Author:    actor,
			Body:      body,
			CreatedAt: now,
		}},
	}
	thread.Comments[0].ThreadID = thread.ID
	bundle.Threads = append(bundle.Threads, thread)
	if err := s.writeBundle(bundle); err != nil {
		return domain.Thread{}, err
	}
	return thread, nil
}

func (s *LocalStorage) ListThreads(ctx context.Context, documentID string, actor string) ([]domain.Thread, error) {
	_ = ctx
	_ = actor
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return nil, err
	}
	threads := make([]domain.Thread, 0, len(bundle.Threads))
	for _, thread := range bundle.Threads {
		thread.Anchor = s.resolveAnchorForDisplayLocked(bundle.Document, thread.Anchor)
		threads = append(threads, thread)
	}
	return threads, nil
}

func (s *LocalStorage) GetThread(ctx context.Context, documentID, threadID, actor string) (domain.Thread, error) {
	_ = ctx
	_ = actor
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return domain.Thread{}, err
	}
	index := threadIndex(bundle.Threads, threadID)
	if index < 0 {
		return domain.Thread{}, domain.NewError(domain.ErrCodeDocumentNotFound, "thread not found", 404)
	}
	thread := bundle.Threads[index]
	thread.Anchor = s.resolveAnchorForDisplayLocked(bundle.Document, thread.Anchor)
	return thread, nil
}

func (s *LocalStorage) ReplyThread(ctx context.Context, documentID, threadID, body, actor string) (domain.Thread, domain.Comment, error) {
	_ = ctx
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return domain.Thread{}, domain.Comment{}, err
	}
	index := threadIndex(bundle.Threads, threadID)
	if index < 0 {
		return domain.Thread{}, domain.Comment{}, domain.NewError(domain.ErrCodeDocumentNotFound, "thread not found", 404)
	}
	now := time.Now().UTC()
	comment := domain.Comment{
		ID:        uuid.NewString(),
		ThreadID:  threadID,
		Author:    actor,
		Body:      body,
		CreatedAt: now,
	}
	bundle.Threads[index].Comments = append(bundle.Threads[index].Comments, comment)
	bundle.Threads[index].UpdatedAt = now
	if err := s.writeBundle(bundle); err != nil {
		return domain.Thread{}, domain.Comment{}, err
	}
	return bundle.Threads[index], comment, nil
}

func (s *LocalStorage) ReanchorThread(ctx context.Context, documentID, threadID string, anchor domain.Anchor, actor string) (domain.Thread, error) {
	_ = ctx
	_ = actor
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return domain.Thread{}, err
	}
	index := threadIndex(bundle.Threads, threadID)
	if index < 0 {
		return domain.Thread{}, domain.NewError(domain.ErrCodeDocumentNotFound, "thread not found", 404)
	}
	resolved, err := s.resolveAnchorStrictLocked(bundle.Document, anchor)
	if err != nil {
		return domain.Thread{}, err
	}
	bundle.Threads[index].Anchor = resolved
	bundle.Threads[index].UpdatedAt = time.Now().UTC()
	if err := s.writeBundle(bundle); err != nil {
		return domain.Thread{}, err
	}
	return bundle.Threads[index], nil
}

func (s *LocalStorage) ReanchorThreadByMatch(ctx context.Context, documentID, threadID string, selector domain.MatchSelector, actor string) (domain.Thread, error) {
	_ = ctx
	_ = actor
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return domain.Thread{}, err
	}
	index := threadIndex(bundle.Threads, threadID)
	if index < 0 {
		return domain.Thread{}, domain.NewError(domain.ErrCodeDocumentNotFound, "thread not found", 404)
	}
	anchor, err := resolveMatchSelector(bundle.Document, selector)
	if err != nil {
		return domain.Thread{}, err
	}
	bundle.Threads[index].Anchor = *anchor
	bundle.Threads[index].UpdatedAt = time.Now().UTC()
	if err := s.writeBundle(bundle); err != nil {
		return domain.Thread{}, err
	}
	return bundle.Threads[index], nil
}

func (s *LocalStorage) SetThreadStatus(ctx context.Context, documentID, threadID string, status domain.ThreadStatus, actor string) (domain.Thread, error) {
	_ = ctx
	_ = actor
	unlock := s.lockDocument(documentID)
	defer unlock()
	bundle, err := s.loadBundle(documentID)
	if err != nil {
		return domain.Thread{}, err
	}
	index := threadIndex(bundle.Threads, threadID)
	if index < 0 {
		return domain.Thread{}, domain.NewError(domain.ErrCodeDocumentNotFound, "thread not found", 404)
	}
	bundle.Threads[index].Status = status
	bundle.Threads[index].UpdatedAt = time.Now().UTC()
	if err := s.writeBundle(bundle); err != nil {
		return domain.Thread{}, err
	}
	return bundle.Threads[index], nil
}

func (s *LocalStorage) applyOpLocked(bundle documentBundle, op collab.Op, actor string) (domain.Document, collab.Op, error) {
	history, err := s.changesSinceLocked(bundle.Document.ID, bundle.Document.Revision, op.BaseRevision)
	if err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	canonical := collab.Rebase(op, history)
	nextSource, err := collab.Apply(bundle.Document.Source, canonical)
	if err != nil {
		return domain.Document{}, collab.Op{}, domain.Wrap(domain.ErrCodeInvalidRequest, 400, err)
	}
	now := time.Now().UTC()
	bundle.Document.Source = nextSource
	bundle.Document.Revision++
	bundle.Document.UpdatedAt = now
	bundle.Document.LastEdited = actor
	bundle.Document.Blocks = docmodel.BuildBlocks(bundle.Document.Format, bundle.Document.Source, bundle.Document.Blocks)
	canonical.BaseRevision = bundle.Document.Revision - 1
	canonical.Author = actor
	if err := s.writeBundle(bundle); err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	s.history[bundle.Document.ID] = append(s.history[bundle.Document.ID], canonical)
	return bundle.Document, canonical, nil
}

func (s *LocalStorage) resolveAnchorStrictLocked(doc domain.Document, anchor domain.Anchor) (domain.Anchor, error) {
	history, err := s.changesSinceLocked(doc.ID, doc.Revision, anchor.Revision)
	if err != nil {
		history = nil
	}
	resolved, resolveErr := docmodel.ResolveAnchor(doc, anchor, history)
	if resolveErr != nil {
		return domain.Anchor{}, domain.NewError(domain.ErrCodeInvalidAnchor, "anchor became stale", 409)
	}
	return resolved, nil
}

func (s *LocalStorage) resolveAnchorForDisplayLocked(doc domain.Document, anchor domain.Anchor) domain.Anchor {
	history, err := s.changesSinceLocked(doc.ID, doc.Revision, anchor.Revision)
	if err != nil {
		history = nil
	}
	resolved, resolveErr := docmodel.ResolveAnchor(doc, anchor, history)
	if resolveErr != nil {
		return resolved
	}
	return resolved
}

func (s *LocalStorage) changesSinceLocked(documentID string, currentRevision, revision int64) ([]collab.Op, error) {
	if revision < 0 {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest, "revision must be non-negative", 400)
	}
	if revision > currentRevision {
		return nil, domain.NewError(domain.ErrCodeStaleVersion, "revision is ahead of the current document", 409)
	}
	appliedCount := int(currentRevision - revision)
	if appliedCount == 0 {
		return nil, nil
	}
	history := s.history[documentID]
	if len(history) < appliedCount {
		return nil, domain.NewError(domain.ErrCodeStaleVersion, "missing edit history for rebase", 409)
	}
	start := len(history) - appliedCount
	out := make([]collab.Op, 0, appliedCount)
	out = append(out, history[start:]...)
	return out, nil
}

func (s *LocalStorage) loadBundle(documentID string) (documentBundle, error) {
	if !validDocumentID(documentID) {
		return documentBundle{}, domain.NewError(domain.ErrCodeDocumentNotFound, "document not found", 404)
	}
	raw, err := os.ReadFile(s.bundlePath(documentID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return documentBundle{}, domain.NewError(domain.ErrCodeDocumentNotFound, "document not found", 404)
		}
		return documentBundle{}, err
	}
	var bundle documentBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return documentBundle{}, err
	}
	if bundle.Version == 0 {
		bundle.Version = bundleVersion
	}
	bundle.Document.Source = normalizeSource(bundle.Document.Source)
	bundle.Document.Blocks = docmodel.BuildBlocks(bundle.Document.Format, bundle.Document.Source, bundle.Document.Blocks)
	if bundle.Threads == nil {
		bundle.Threads = []domain.Thread{}
	}
	if bundle.Annotations == nil {
		bundle.Annotations = []domain.Annotation{}
	}
	if bundle.Suggestions == nil {
		bundle.Suggestions = []domain.SuggestionBatch{}
	}
	if bundle.Imports == nil {
		bundle.Imports = []domain.ImportRecord{}
	}
	return bundle, nil
}

func (s *LocalStorage) writeBundle(bundle documentBundle) error {
	if !validDocumentID(bundle.Document.ID) {
		return domain.NewError(domain.ErrCodeInvalidRequest, "invalid document id", 400)
	}
	bundle.Version = bundleVersion
	bundle.Document.Source = normalizeSource(bundle.Document.Source)
	bundle.Document.Blocks = docmodel.BuildBlocks(bundle.Document.Format, bundle.Document.Source, bundle.Document.Blocks)
	body, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicWriteFile(s.bundlePath(bundle.Document.ID), body, 0o644)
}

func (s *LocalStorage) bundlePath(documentID string) string {
	return filepath.Join(s.root, "documents", documentID+".json")
}

func (s *LocalStorage) documentLock(documentID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, ok := s.docLocks[documentID]
	if !ok {
		lock = &sync.Mutex{}
		s.docLocks[documentID] = lock
	}
	return lock
}

func (s *LocalStorage) lockDocument(documentID string) func() {
	lock := s.documentLock(documentID)
	lock.Lock()
	return lock.Unlock
}

func resolveMatchSelector(doc domain.Document, selector domain.MatchSelector) (*domain.Anchor, error) {
	if strings.TrimSpace(selector.Match) == "" {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest, "match is required", 400)
	}
	if selector.Occurrence < 0 {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest, "occurrence must be positive", 400)
	}

	source := []rune(doc.Source)
	match := []rune(selector.Match)
	before := []rune(selector.Before)
	after := []rune(selector.After)
	candidates := make([][2]int, 0)
	for idx := 0; idx+len(match) <= len(source); idx++ {
		if string(source[idx:idx+len(match)]) != selector.Match {
			continue
		}
		if len(before) > 0 {
			if idx < len(before) || string(source[idx-len(before):idx]) != selector.Before {
				continue
			}
		}
		end := idx + len(match)
		if len(after) > 0 {
			if end+len(after) > len(source) || string(source[end:end+len(after)]) != selector.After {
				continue
			}
		}
		candidates = append(candidates, [2]int{idx, end})
	}
	if len(candidates) == 0 {
		return nil, domain.NewError(domain.ErrCodeDocumentNotFound, "match not found", 404)
	}
	if selector.Occurrence > 0 {
		if selector.Occurrence > len(candidates) {
			return nil, domain.NewError(domain.ErrCodeDocumentNotFound, "requested occurrence not found", 404)
		}
		selection := candidates[selector.Occurrence-1]
		return docmodel.AnchorFromSelection(doc, selection[0], selection[1])
	}
	if len(candidates) > 1 {
		return nil, domain.NewError(domain.ErrCodeConflict, "match is ambiguous; add before/after context or occurrence", 409)
	}
	return docmodel.AnchorFromSelection(doc, candidates[0][0], candidates[0][1])
}

func relevantBlocks(blocks []domain.Block, start, end int) []domain.Block {
	relevant := make([]domain.Block, 0, len(blocks))
	for _, block := range blocks {
		if block.End <= start || block.Start >= end {
			continue
		}
		relevant = append(relevant, block)
	}
	return relevant
}

func threadIndex(threads []domain.Thread, threadID string) int {
	for i, thread := range threads {
		if thread.ID == threadID {
			return i
		}
	}
	return -1
}

func normalizeSource(source string) string {
	return strings.ReplaceAll(source, "\r\n", "\n")
}

func validDocumentID(documentID string) bool {
	return strings.TrimSpace(documentID) != "" && !strings.Contains(documentID, "/") && !strings.Contains(documentID, string(filepath.Separator))
}

func resolveStorageRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, ".agentpad")
	}
	if !filepath.IsAbs(path) {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path = absPath
	}
	return filepath.Clean(path), nil
}

func atomicWriteFile(path string, body []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := fmt.Sprintf("%s.%s.tmp", path, uuid.NewString())
	if err := os.WriteFile(tmpPath, body, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
