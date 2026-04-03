package store

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cyrusaf/agentpad/internal/collab"
	"github.com/cyrusaf/agentpad/internal/docmodel"
	"github.com/cyrusaf/agentpad/internal/domain"
	"github.com/cyrusaf/agentpad/internal/importexport"
)

const (
	sidecarVersion = 1
	indexVersion   = 1
)

type ReadOptions struct {
	Full    bool
	BlockID string
	Start   int
	End     int
	Query   string
	Quote   string
	Prefix  string
	Suffix  string
}

type Store struct {
	root string

	mu       sync.Mutex
	docLocks map[string]*sync.Mutex
}

type sidecarDocument struct {
	Title      string                `json:"title"`
	Format     domain.DocumentFormat `json:"format"`
	Revision   int64                 `json:"revision"`
	CreatedAt  time.Time             `json:"created_at"`
	UpdatedAt  time.Time             `json:"updated_at"`
	LastEdited string                `json:"last_edited,omitempty"`
}

type sidecar struct {
	Version     int                      `json:"version"`
	Document    sidecarDocument          `json:"document"`
	Threads     []domain.Thread          `json:"threads"`
	Annotations []domain.Annotation      `json:"annotations"`
	Suggestions []domain.SuggestionBatch `json:"suggestions"`
	Imports     []domain.ImportRecord    `json:"imports"`
}

type opRecord struct {
	ID         string    `json:"id"`
	DocumentID string    `json:"document_id"`
	Revision   int64     `json:"revision"`
	Actor      string    `json:"actor"`
	CreatedAt  time.Time `json:"created_at"`
	Op         collab.Op `json:"op"`
}

type fileIdentity struct {
	Available bool   `json:"available,omitempty"`
	Device    uint64 `json:"device,omitempty"`
	Inode     uint64 `json:"inode,omitempty"`
}

type indexFile struct {
	Version   int          `json:"version"`
	Documents []indexEntry `json:"documents"`
}

type indexEntry struct {
	Key          string       `json:"key"`
	Path         string       `json:"path"`
	LastSeenPath string       `json:"last_seen_path,omitempty"`
	Identity     fileIdentity `json:"identity"`
}

type documentRef struct {
	Key          string
	Path         string
	PreviousPath string
	Identity     fileIdentity
}

func Open(root string) (*Store, error) {
	resolvedRoot, err := resolveMetadataRoot(root)
	if err != nil {
		return nil, err
	}
	return &Store{
		root:     resolvedRoot,
		docLocks: map[string]*sync.Mutex{},
	}, nil
}

func (s *Store) Close() error { return nil }

func (s *Store) OpenDocument(ctx context.Context, path, actor string) (domain.Document, error) {
	return s.GetDocument(ctx, path, actor)
}

func (s *Store) GetDocument(ctx context.Context, id, actor string) (domain.Document, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(id)
	if err != nil {
		return domain.Document{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	doc, _, err := s.loadDocumentData(ref, actor, true)
	return doc, err
}

func (s *Store) ReadDocument(ctx context.Context, id, actor string, opts ReadOptions) (domain.DocumentRead, error) {
	doc, err := s.GetDocument(ctx, id, actor)
	if err != nil {
		return domain.DocumentRead{}, err
	}
	result := domain.DocumentRead{
		DocumentID: id,
		Revision:   doc.Revision,
		Scope:      "full",
		Text:       doc.Source,
	}
	if opts.Full {
		result.Blocks = doc.Blocks
	}
	switch {
	case opts.Quote != "":
		result.Scope = "quote"
		anchor, err := anchorForQuote(doc, opts.Quote, opts.Prefix, opts.Suffix, opts.BlockID)
		if err != nil {
			return domain.DocumentRead{}, err
		}
		result.Text = anchor.Quote
		if opts.Full {
			result.Blocks = relevantBlocks(doc.Blocks, anchor.DocStart, anchor.DocEnd)
		} else {
			result.Blocks = nil
		}
		result.Anchor = anchor
		return result, nil
	case opts.Query != "":
		result.Scope = "query"
		runes := []rune(doc.Source)
		idx := strings.Index(strings.ToLower(doc.Source), strings.ToLower(opts.Query))
		if idx < 0 {
			result.Text = ""
			result.Blocks = nil
			return result, nil
		}
		start := utf8Index(doc.Source, idx)
		end := start + len([]rune(opts.Query))
		result.Text = string(runes[max(0, start-80):min(len(runes), end+80)])
		if opts.Full {
			result.Blocks = relevantBlocks(doc.Blocks, start, end)
		} else {
			result.Blocks = nil
		}
	case opts.BlockID != "":
		result.Scope = "block"
		for _, block := range doc.Blocks {
			if block.ID == opts.BlockID {
				result.Text = block.Text
				if opts.Full {
					result.Blocks = []domain.Block{block}
				} else {
					result.Blocks = nil
				}
				anchor, err := docmodel.AnchorFromSelection(doc, block.Start, block.End)
				if err != nil {
					return domain.DocumentRead{}, err
				}
				result.Anchor = anchor
				return result, nil
			}
		}
		return domain.DocumentRead{}, domain.NewError(domain.ErrCodeInvalidRequest, "block not found", 404)
	case opts.Start >= 0 && opts.End >= opts.Start:
		result.Scope = "range"
		runes := []rune(doc.Source)
		if opts.End > len(runes) {
			return domain.DocumentRead{}, domain.NewError(domain.ErrCodeInvalidRequest, "range is out of bounds", 400)
		}
		result.Text = string(runes[opts.Start:opts.End])
		if opts.Full {
			result.Blocks = relevantBlocks(doc.Blocks, opts.Start, opts.End)
		} else {
			result.Blocks = nil
		}
		anchor, err := docmodel.AnchorFromSelection(doc, opts.Start, opts.End)
		if err != nil {
			return domain.DocumentRead{}, err
		}
		result.Anchor = anchor
	case opts.Full:
		return result, nil
	}
	return result, nil
}

func (s *Store) ChangesSince(ctx context.Context, documentID string, revision int64) ([]collab.Op, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return nil, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	return s.changesSinceUnlocked(ref, revision)
}

func (s *Store) ApplyOp(ctx context.Context, documentID string, op collab.Op, actor string) (domain.Document, collab.Op, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	return s.applyOpUnlocked(ref, actor, op)
}

func (s *Store) ApplyAnchorEdit(ctx context.Context, documentID string, anchor domain.Anchor, replacement, actor string) (domain.Document, collab.Op, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	doc, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	_ = data
	history, err := s.changesSinceUnlocked(ref, anchor.Revision)
	if err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	resolved, err := docmodel.ResolveAnchor(doc, anchor, history)
	if err != nil {
		return domain.Document{}, collab.Op{}, domain.NewError(domain.ErrCodeInvalidAnchor, "anchor became stale", 409)
	}
	return s.applyOpUnlocked(ref, actor, collab.Op{
		Position:     resolved.DocStart,
		DeleteCount:  resolved.DocEnd - resolved.DocStart,
		InsertText:   replacement,
		BaseRevision: doc.Revision,
		Author:       actor,
	})
}

func (s *Store) ApplyThreadEdit(ctx context.Context, documentID, threadID, replacement, actor string) (domain.Thread, domain.Document, collab.Op, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return domain.Thread{}, domain.Document{}, collab.Op{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()

	doc, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.Thread{}, domain.Document{}, collab.Op{}, err
	}
	index := threadIndex(data.Threads, threadID)
	if index < 0 {
		return domain.Thread{}, domain.Document{}, collab.Op{}, domain.NewError(domain.ErrCodeDocumentNotFound, "thread not found", 404)
	}

	resolved, err := s.resolveAnchorStrictUnlocked(ref, doc, data.Threads[index].Anchor)
	if err != nil {
		return domain.Thread{}, domain.Document{}, collab.Op{}, domain.NewError(domain.ErrCodeInvalidAnchor, "thread anchor became stale", 409)
	}
	updatedDoc, canonical, err := s.applyOpUnlocked(ref, actor, collab.Op{
		Position:     resolved.DocStart,
		DeleteCount:  resolved.DocEnd - resolved.DocStart,
		InsertText:   replacement,
		BaseRevision: doc.Revision,
		Author:       actor,
	})
	if err != nil {
		return domain.Thread{}, domain.Document{}, collab.Op{}, err
	}

	_, data, err = s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.Thread{}, domain.Document{}, collab.Op{}, err
	}
	index = threadIndex(data.Threads, threadID)
	if index < 0 {
		return domain.Thread{}, domain.Document{}, collab.Op{}, domain.NewError(domain.ErrCodeDocumentNotFound, "thread not found", 404)
	}
	newAnchor, err := docmodel.AnchorFromSelection(updatedDoc, canonical.Position, canonical.Position+len([]rune(replacement)))
	if err != nil {
		return domain.Thread{}, domain.Document{}, collab.Op{}, err
	}
	data.Threads[index].DocumentID = ref.Path
	data.Threads[index].Anchor = *newAnchor
	data.Threads[index].UpdatedAt = time.Now().UTC()
	if err := s.writeSidecar(ref, data); err != nil {
		return domain.Thread{}, domain.Document{}, collab.Op{}, err
	}

	return data.Threads[index], updatedDoc, canonical, nil
}

func (s *Store) applyOpUnlocked(ref documentRef, actor string, op collab.Op) (domain.Document, collab.Op, error) {
	doc, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	history, err := s.changesSinceUnlocked(ref, op.BaseRevision)
	if err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	canonical := collab.Rebase(op, history)
	nextSource, err := collab.Apply(doc.Source, canonical)
	if err != nil {
		return domain.Document{}, collab.Op{}, domain.Wrap(domain.ErrCodeInvalidRequest, 400, err)
	}
	canonical.BaseRevision = doc.Revision
	now := time.Now().UTC()
	nextRevision := doc.Revision + 1
	if err := atomicWriteFile(ref.Path, []byte(nextSource), filePerm(ref.Path)); err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	data.Document.Revision = nextRevision
	data.Document.UpdatedAt = now
	data.Document.LastEdited = actor
	if err := s.writeSidecar(ref, data); err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	if err := s.appendOpLocked(ref, opRecord{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Revision:   nextRevision,
		Actor:      actor,
		CreatedAt:  now,
		Op:         canonical,
	}); err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	if err := s.appendActivityLocked(ref, domain.ActivityEvent{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Type:       "document.edited",
		Actor:      actor,
		Payload: map[string]any{
			"revision": nextRevision,
			"op":       canonical,
		},
		CreatedAt: now,
	}); err != nil {
		return domain.Document{}, collab.Op{}, err
	}
	return s.buildDocument(ref.Path, nextSource, data), canonical, nil
}

func (s *Store) CreateThread(ctx context.Context, documentID string, anchor domain.Anchor, body, actor string) (domain.Thread, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return domain.Thread{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	doc, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.Thread{}, err
	}
	history, err := s.changesSinceUnlocked(ref, anchor.Revision)
	if err != nil {
		return domain.Thread{}, err
	}
	resolved, err := docmodel.ResolveAnchor(doc, anchor, history)
	if err != nil {
		return domain.Thread{}, err
	}
	now := time.Now().UTC()
	thread := domain.Thread{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Anchor:     resolved,
		Status:     domain.ThreadStatusOpen,
		Author:     actor,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	thread.Comments = []domain.Comment{{
		ID:        uuid.NewString(),
		ThreadID:  thread.ID,
		Author:    actor,
		Body:      body,
		CreatedAt: now,
	}}
	data.Threads = append(data.Threads, thread)
	if err := s.writeSidecar(ref, data); err != nil {
		return domain.Thread{}, err
	}
	if err := s.appendActivityLocked(ref, domain.ActivityEvent{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Type:       "thread.created",
		Actor:      actor,
		Payload:    map[string]any{"thread_id": thread.ID},
		CreatedAt:  now,
	}); err != nil {
		return domain.Thread{}, err
	}
	return thread, nil
}

func (s *Store) ListThreads(ctx context.Context, documentID string, actor string) ([]domain.Thread, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return nil, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	doc, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return nil, err
	}
	threads := make([]domain.Thread, 0, len(data.Threads))
	for _, item := range data.Threads {
		item.DocumentID = ref.Path
		resolved, err := s.resolveAnchorForDisplayUnlocked(ref, doc, item.Anchor)
		if err != nil {
			return nil, err
		}
		item.Anchor = resolved
		threads = append(threads, item)
	}
	return threads, nil
}

func (s *Store) GetThread(ctx context.Context, documentID, threadID, actor string) (domain.Thread, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return domain.Thread{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	doc, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.Thread{}, err
	}
	index := threadIndex(data.Threads, threadID)
	if index < 0 {
		return domain.Thread{}, domain.NewError(domain.ErrCodeDocumentNotFound, "thread not found", 404)
	}
	thread := data.Threads[index]
	thread.DocumentID = ref.Path
	resolved, err := s.resolveAnchorForDisplayUnlocked(ref, doc, thread.Anchor)
	if err != nil {
		return domain.Thread{}, err
	}
	thread.Anchor = resolved
	return thread, nil
}

func (s *Store) ReplyThread(ctx context.Context, documentID, threadID, body, actor string) (domain.Thread, domain.Comment, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return domain.Thread{}, domain.Comment{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	_, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.Thread{}, domain.Comment{}, err
	}
	index := threadIndex(data.Threads, threadID)
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
	data.Threads[index].DocumentID = ref.Path
	data.Threads[index].Comments = append(data.Threads[index].Comments, comment)
	data.Threads[index].UpdatedAt = now
	if err := s.writeSidecar(ref, data); err != nil {
		return domain.Thread{}, domain.Comment{}, err
	}
	if err := s.appendActivityLocked(ref, domain.ActivityEvent{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Type:       "thread.replied",
		Actor:      actor,
		Payload:    map[string]any{"thread_id": threadID},
		CreatedAt:  now,
	}); err != nil {
		return domain.Thread{}, domain.Comment{}, err
	}
	return data.Threads[index], comment, nil
}

func (s *Store) ReanchorThread(ctx context.Context, documentID, threadID string, anchor domain.Anchor, actor string) (domain.Thread, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return domain.Thread{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	doc, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.Thread{}, err
	}
	index := threadIndex(data.Threads, threadID)
	if index < 0 {
		return domain.Thread{}, domain.NewError(domain.ErrCodeDocumentNotFound, "thread not found", 404)
	}
	history, err := s.changesSinceUnlocked(ref, anchor.Revision)
	if err != nil {
		return domain.Thread{}, err
	}
	resolved, err := docmodel.ResolveAnchor(doc, anchor, history)
	if err != nil {
		return domain.Thread{}, err
	}
	now := time.Now().UTC()
	data.Threads[index].DocumentID = ref.Path
	data.Threads[index].Anchor = resolved
	data.Threads[index].UpdatedAt = now
	if err := s.writeSidecar(ref, data); err != nil {
		return domain.Thread{}, err
	}
	if err := s.appendActivityLocked(ref, domain.ActivityEvent{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Type:       "thread.reanchored",
		Actor:      actor,
		Payload:    map[string]any{"thread_id": threadID},
		CreatedAt:  now,
	}); err != nil {
		return domain.Thread{}, err
	}
	return data.Threads[index], nil
}

func (s *Store) SetThreadStatus(ctx context.Context, documentID, threadID string, status domain.ThreadStatus, actor string) (domain.Thread, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return domain.Thread{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	_, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.Thread{}, err
	}
	index := threadIndex(data.Threads, threadID)
	if index < 0 {
		return domain.Thread{}, domain.NewError(domain.ErrCodeDocumentNotFound, "thread not found", 404)
	}
	now := time.Now().UTC()
	data.Threads[index].DocumentID = ref.Path
	data.Threads[index].Status = status
	data.Threads[index].UpdatedAt = now
	if err := s.writeSidecar(ref, data); err != nil {
		return domain.Thread{}, err
	}
	eventType := "thread.reopened"
	if status == domain.ThreadStatusResolved {
		eventType = "thread.resolved"
	}
	if err := s.appendActivityLocked(ref, domain.ActivityEvent{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Type:       eventType,
		Actor:      actor,
		Payload:    map[string]any{"thread_id": threadID},
		CreatedAt:  now,
	}); err != nil {
		return domain.Thread{}, err
	}
	return data.Threads[index], nil
}

func (s *Store) CreateAnnotation(ctx context.Context, documentID string, annotation domain.Annotation, actor string) (domain.Annotation, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return domain.Annotation{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	doc, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.Annotation{}, err
	}
	if annotation.Anchor != nil {
		history, err := s.changesSinceUnlocked(ref, annotation.Anchor.Revision)
		if err != nil {
			return domain.Annotation{}, err
		}
		resolved, err := docmodel.ResolveAnchor(doc, *annotation.Anchor, history)
		if err != nil {
			return domain.Annotation{}, err
		}
		annotation.Anchor = &resolved
	}
	if annotation.Metadata == nil {
		annotation.Metadata = map[string]any{}
	}
	now := time.Now().UTC()
	annotation.ID = uuid.NewString()
	annotation.DocumentID = ref.Path
	annotation.Author = actor
	annotation.CreatedAt = now
	annotation.UpdatedAt = now
	data.Annotations = append(data.Annotations, annotation)
	if err := s.writeSidecar(ref, data); err != nil {
		return domain.Annotation{}, err
	}
	if err := s.appendActivityLocked(ref, domain.ActivityEvent{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Type:       "annotation.created",
		Actor:      actor,
		Payload:    map[string]any{"annotation_id": annotation.ID, "kind": annotation.Kind},
		CreatedAt:  now,
	}); err != nil {
		return domain.Annotation{}, err
	}
	return annotation, nil
}

func (s *Store) ListAnnotations(ctx context.Context, documentID, actor string) ([]domain.Annotation, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return nil, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	doc, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return nil, err
	}
	items := make([]domain.Annotation, 0, len(data.Annotations))
	for _, item := range data.Annotations {
		item.DocumentID = ref.Path
		if item.Anchor != nil {
			resolved, err := s.resolveAnchorForDisplayUnlocked(ref, doc, *item.Anchor)
			if err != nil {
				return nil, err
			}
			item.Anchor = &resolved
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Store) UpdateAnnotation(ctx context.Context, documentID, annotationID, body string, metadata map[string]any, actor string) (domain.Annotation, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return domain.Annotation{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	_, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.Annotation{}, err
	}
	index := annotationIndex(data.Annotations, annotationID)
	if index < 0 {
		return domain.Annotation{}, domain.NewError(domain.ErrCodeDocumentNotFound, "annotation not found", 404)
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	now := time.Now().UTC()
	data.Annotations[index].DocumentID = ref.Path
	data.Annotations[index].Body = body
	data.Annotations[index].Metadata = metadata
	data.Annotations[index].UpdatedAt = now
	if err := s.writeSidecar(ref, data); err != nil {
		return domain.Annotation{}, err
	}
	if err := s.appendActivityLocked(ref, domain.ActivityEvent{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Type:       "annotation.updated",
		Actor:      actor,
		Payload:    map[string]any{"annotation_id": annotationID},
		CreatedAt:  now,
	}); err != nil {
		return domain.Annotation{}, err
	}
	return data.Annotations[index], nil
}

func (s *Store) DeleteAnnotation(ctx context.Context, documentID, annotationID, actor string) error {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	_, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return err
	}
	index := annotationIndex(data.Annotations, annotationID)
	if index < 0 {
		return domain.NewError(domain.ErrCodeDocumentNotFound, "annotation not found", 404)
	}
	data.Annotations = append(data.Annotations[:index], data.Annotations[index+1:]...)
	if err := s.writeSidecar(ref, data); err != nil {
		return err
	}
	return s.appendActivityLocked(ref, domain.ActivityEvent{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Type:       "annotation.deleted",
		Actor:      actor,
		Payload:    map[string]any{"annotation_id": annotationID},
		CreatedAt:  time.Now().UTC(),
	})
}

func (s *Store) CreateSuggestionBatch(ctx context.Context, documentID string, batch domain.SuggestionBatch, actor string) (domain.SuggestionBatch, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return domain.SuggestionBatch{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	_, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.SuggestionBatch{}, err
	}
	now := time.Now().UTC()
	batch.ID = uuid.NewString()
	batch.DocumentID = ref.Path
	batch.Status = domain.SuggestionStatusPending
	batch.Author = actor
	batch.CreatedAt = now
	batch.UpdatedAt = now
	for i := range batch.Ops {
		if batch.Ops[i].ID == "" {
			batch.Ops[i].ID = uuid.NewString()
		}
	}
	data.Suggestions = append(data.Suggestions, batch)
	if err := s.writeSidecar(ref, data); err != nil {
		return domain.SuggestionBatch{}, err
	}
	if err := s.appendActivityLocked(ref, domain.ActivityEvent{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Type:       "suggestion.created",
		Actor:      actor,
		Payload:    map[string]any{"suggestion_batch_id": batch.ID},
		CreatedAt:  now,
	}); err != nil {
		return domain.SuggestionBatch{}, err
	}
	return batch, nil
}

func (s *Store) ListSuggestionBatches(ctx context.Context, documentID, actor string) ([]domain.SuggestionBatch, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return nil, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	_, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return nil, err
	}
	items := make([]domain.SuggestionBatch, len(data.Suggestions))
	copy(items, data.Suggestions)
	for i := range items {
		items[i].DocumentID = ref.Path
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, nil
}

func (s *Store) ApplySuggestionBatch(ctx context.Context, documentID, batchID, actor string) (domain.SuggestionBatch, domain.Document, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return domain.SuggestionBatch{}, domain.Document{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	doc, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.SuggestionBatch{}, domain.Document{}, err
	}
	index := suggestionIndex(data.Suggestions, batchID)
	if index < 0 {
		return domain.SuggestionBatch{}, domain.Document{}, domain.NewError(domain.ErrCodeDocumentNotFound, "suggestion batch not found", 404)
	}
	batch := data.Suggestions[index]
	if batch.Status != domain.SuggestionStatusPending {
		return domain.SuggestionBatch{}, domain.Document{}, domain.NewError(domain.ErrCodeInvalidSuggestion, "suggestion batch is not pending", 400)
	}
	ops := append([]domain.SuggestionOp(nil), batch.Ops...)
	sort.SliceStable(ops, func(i, j int) bool {
		return ops[i].Anchor.DocStart > ops[j].Anchor.DocStart
	})
	now := time.Now().UTC()
	source := doc.Source
	revision := doc.Revision
	for _, suggestion := range ops {
		docHistory, err := s.changesSinceUnlocked(ref, suggestion.Anchor.Revision)
		if err != nil {
			return domain.SuggestionBatch{}, domain.Document{}, err
		}
		workingDoc := s.buildDocument(ref.Path, source, data)
		workingDoc.Revision = revision
		resolved, err := docmodel.ResolveAnchor(workingDoc, suggestion.Anchor, docHistory)
		if err != nil {
			data.Suggestions[index].Status = domain.SuggestionStatusStale
			data.Suggestions[index].Conflict = err.Error()
			data.Suggestions[index].UpdatedAt = now
			_ = s.writeSidecar(ref, data)
			return domain.SuggestionBatch{}, domain.Document{}, domain.NewError(domain.ErrCodeInvalidSuggestion, "suggestion batch became stale", 409)
		}
		op := collab.Op{
			Position:     resolved.DocStart,
			DeleteCount:  resolved.DocEnd - resolved.DocStart,
			InsertText:   suggestion.Replacement,
			BaseRevision: revision,
			Author:       actor,
		}
		source, err = collab.Apply(source, op)
		if err != nil {
			return domain.SuggestionBatch{}, domain.Document{}, err
		}
		revision++
		if err := s.appendOpLocked(ref, opRecord{
			ID:         uuid.NewString(),
			DocumentID: ref.Path,
			Revision:   revision,
			Actor:      actor,
			CreatedAt:  now,
			Op:         op,
		}); err != nil {
			return domain.SuggestionBatch{}, domain.Document{}, err
		}
	}
	if err := atomicWriteFile(ref.Path, []byte(source), filePerm(ref.Path)); err != nil {
		return domain.SuggestionBatch{}, domain.Document{}, err
	}
	data.Document.Revision = revision
	data.Document.UpdatedAt = now
	data.Document.LastEdited = actor
	data.Suggestions[index].DocumentID = ref.Path
	data.Suggestions[index].Status = domain.SuggestionStatusAccepted
	data.Suggestions[index].UpdatedAt = now
	if err := s.writeSidecar(ref, data); err != nil {
		return domain.SuggestionBatch{}, domain.Document{}, err
	}
	if err := s.appendActivityLocked(ref, domain.ActivityEvent{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Type:       "suggestion.accepted",
		Actor:      actor,
		Payload:    map[string]any{"suggestion_batch_id": batchID},
		CreatedAt:  now,
	}); err != nil {
		return domain.SuggestionBatch{}, domain.Document{}, err
	}
	return data.Suggestions[index], s.buildDocument(ref.Path, source, data), nil
}

func (s *Store) RejectSuggestionBatch(ctx context.Context, documentID, batchID, actor string) (domain.SuggestionBatch, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return domain.SuggestionBatch{}, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	_, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return domain.SuggestionBatch{}, err
	}
	index := suggestionIndex(data.Suggestions, batchID)
	if index < 0 {
		return domain.SuggestionBatch{}, domain.NewError(domain.ErrCodeDocumentNotFound, "suggestion batch not found", 404)
	}
	now := time.Now().UTC()
	data.Suggestions[index].DocumentID = ref.Path
	data.Suggestions[index].Status = domain.SuggestionStatusRejected
	data.Suggestions[index].UpdatedAt = now
	if err := s.writeSidecar(ref, data); err != nil {
		return domain.SuggestionBatch{}, err
	}
	if err := s.appendActivityLocked(ref, domain.ActivityEvent{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Type:       "suggestion.rejected",
		Actor:      actor,
		Payload:    map[string]any{"suggestion_batch_id": batchID},
		CreatedAt:  now,
	}); err != nil {
		return domain.SuggestionBatch{}, err
	}
	return data.Suggestions[index], nil
}

func (s *Store) Activity(ctx context.Context, documentID string) ([]domain.ActivityEvent, error) {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return nil, err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	items, err := readNDJSON[domain.ActivityEvent](s.activityLogPath(ref))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for i := range items {
		items[i].DocumentID = ref.Path
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items, nil
}

func (s *Store) RecordImport(ctx context.Context, documentID, actor string, imported importexport.Imported) error {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	_, data, err := s.loadDocumentData(ref, actor, true)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	data.Imports = append(data.Imports, domain.ImportRecord{
		ID:           uuid.NewString(),
		DocumentID:   ref.Path,
		SourcePath:   imported.SourcePath,
		SourceFormat: imported.SourceFormat,
		Warnings:     imported.Warnings,
		CreatedAt:    now,
	})
	if err := s.writeSidecar(ref, data); err != nil {
		return err
	}
	return s.appendActivityLocked(ref, domain.ActivityEvent{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Type:       "document.imported",
		Actor:      actor,
		Payload: map[string]any{
			"path":     imported.SourcePath,
			"warnings": imported.Warnings,
		},
		CreatedAt: now,
	})
}

func (s *Store) RecordActivity(ctx context.Context, documentID, eventType, actor string, payload map[string]any) error {
	_ = ctx
	ref, err := s.resolveDocumentRef(documentID)
	if err != nil {
		return err
	}
	unlock := s.lockDocument(ref.Key)
	defer unlock()
	return s.appendActivityLocked(ref, domain.ActivityEvent{
		ID:         uuid.NewString(),
		DocumentID: ref.Path,
		Type:       eventType,
		Actor:      actor,
		Payload:    payload,
		CreatedAt:  time.Now().UTC(),
	})
}

func (s *Store) loadDocumentData(ref documentRef, actor string, createSidecars bool) (domain.Document, sidecar, error) {
	sourceBytes, err := os.ReadFile(ref.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.Document{}, sidecar{}, domain.NewError(domain.ErrCodeDocumentNotFound, "document not found", 404)
		}
		return domain.Document{}, sidecar{}, err
	}
	source := normalizeSource(string(sourceBytes))
	data, err := s.readOrCreateSidecar(ref, actor, detectFormat(ref.Path), createSidecars)
	if err != nil {
		return domain.Document{}, sidecar{}, err
	}
	return s.buildDocument(ref.Path, source, data), data, nil
}

func (s *Store) buildDocument(path, source string, data sidecar) domain.Document {
	format := data.Document.Format
	if format == "" {
		format = detectFormat(path)
	}
	return domain.Document{
		ID:         path,
		Title:      data.Document.Title,
		Format:     format,
		Source:     source,
		Revision:   data.Document.Revision,
		Blocks:     docmodel.BuildBlocks(format, source, nil),
		CreatedAt:  data.Document.CreatedAt,
		UpdatedAt:  data.Document.UpdatedAt,
		LastEdited: data.Document.LastEdited,
	}
}

func (s *Store) readOrCreateSidecar(ref documentRef, actor string, format domain.DocumentFormat, create bool) (sidecar, error) {
	metaPath := s.metaPath(ref)
	raw, err := os.ReadFile(metaPath)
	if err == nil {
		var data sidecar
		if err := json.Unmarshal(raw, &data); err != nil {
			return sidecar{}, err
		}
		changed := false
		if data.Version == 0 {
			data.Version = sidecarVersion
			changed = true
		}
		if data.Document.Title == "" {
			data.Document.Title = defaultTitle(ref.Path)
			changed = true
		} else if shouldRefreshTitle(data.Document.Title, ref.PreviousPath, ref.Path) {
			data.Document.Title = defaultTitle(ref.Path)
			changed = true
		}
		if data.Document.Format == "" {
			data.Document.Format = format
			changed = true
		}
		if changed {
			if err := s.writeSidecar(ref, data); err != nil {
				return sidecar{}, err
			}
		}
		if err := touchIfMissing(s.activityLogPath(ref)); err != nil {
			return sidecar{}, err
		}
		if err := touchIfMissing(s.opLogPath(ref)); err != nil {
			return sidecar{}, err
		}
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return sidecar{}, err
	}
	if !create {
		return sidecar{}, domain.NewError(domain.ErrCodeDocumentNotFound, "document not found", 404)
	}
	now := time.Now().UTC()
	data := sidecar{
		Version: sidecarVersion,
		Document: sidecarDocument{
			Title:      defaultTitle(ref.Path),
			Format:     format,
			Revision:   0,
			CreatedAt:  now,
			UpdatedAt:  now,
			LastEdited: actor,
		},
		Threads:     []domain.Thread{},
		Annotations: []domain.Annotation{},
		Suggestions: []domain.SuggestionBatch{},
		Imports:     []domain.ImportRecord{},
	}
	if err := s.writeSidecar(ref, data); err != nil {
		return sidecar{}, err
	}
	if err := touchIfMissing(s.activityLogPath(ref)); err != nil {
		return sidecar{}, err
	}
	if err := touchIfMissing(s.opLogPath(ref)); err != nil {
		return sidecar{}, err
	}
	return data, nil
}

func (s *Store) writeSidecar(ref documentRef, data sidecar) error {
	body, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicWriteFile(s.metaPath(ref), body, 0o644)
}

func (s *Store) changesSinceUnlocked(ref documentRef, revision int64) ([]collab.Op, error) {
	records, err := readNDJSON[opRecord](s.opLogPath(ref))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ops := make([]collab.Op, 0, len(records))
	for _, record := range records {
		if record.Revision > revision {
			ops = append(ops, record.Op)
		}
	}
	return ops, nil
}

func (s *Store) appendActivityLocked(ref documentRef, event domain.ActivityEvent) error {
	if event.Payload == nil {
		event.Payload = map[string]any{}
	}
	return appendNDJSON(s.activityLogPath(ref), event)
}

func (s *Store) appendOpLocked(ref documentRef, record opRecord) error {
	return appendNDJSON(s.opLogPath(ref), record)
}

func (s *Store) resolveDocumentRef(path string) (documentRef, error) {
	cleanPath, info, err := validateDocumentPath(path)
	if err != nil {
		return documentRef{}, err
	}
	identity := fileIdentityFromInfo(info)

	s.mu.Lock()
	defer s.mu.Unlock()

	index, err := s.readIndexLocked()
	if err != nil {
		return documentRef{}, err
	}

	entryIndex := -1
	if identity.Available {
		for i, item := range index.Documents {
			if sameIdentity(item.Identity, identity) {
				entryIndex = i
				break
			}
		}
	}
	if entryIndex < 0 {
		for i, item := range index.Documents {
			if item.Path == cleanPath {
				entryIndex = i
				break
			}
		}
	}

	var entry indexEntry
	changed := false
	if entryIndex >= 0 {
		entry = index.Documents[entryIndex]
	} else {
		entry = indexEntry{
			Key:          uuid.NewString(),
			Path:         cleanPath,
			LastSeenPath: cleanPath,
			Identity:     identity,
		}
		index.Documents = append(index.Documents, entry)
		entryIndex = len(index.Documents) - 1
		changed = true
	}

	previousPath := entry.Path
	if previousPath == "" {
		previousPath = cleanPath
	}
	if entry.Path != cleanPath {
		entry.Path = cleanPath
		changed = true
	}
	if entry.LastSeenPath != cleanPath {
		entry.LastSeenPath = cleanPath
		changed = true
	}
	if entry.Identity != identity {
		entry.Identity = identity
		changed = true
	}
	if changed {
		index.Documents[entryIndex] = entry
		if err := s.writeIndexLocked(index); err != nil {
			return documentRef{}, err
		}
	}

	return documentRef{
		Key:          entry.Key,
		Path:         cleanPath,
		PreviousPath: previousPath,
		Identity:     identity,
	}, nil
}

func (s *Store) readIndexLocked() (indexFile, error) {
	raw, err := os.ReadFile(s.indexPath())
	if err == nil {
		var index indexFile
		if err := json.Unmarshal(raw, &index); err != nil {
			return indexFile{}, err
		}
		if index.Version == 0 {
			index.Version = indexVersion
		}
		if index.Documents == nil {
			index.Documents = []indexEntry{}
		}
		return index, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return indexFile{Version: indexVersion, Documents: []indexEntry{}}, nil
	}
	return indexFile{}, err
}

func (s *Store) writeIndexLocked(index indexFile) error {
	if index.Version == 0 {
		index.Version = indexVersion
	}
	if index.Documents == nil {
		index.Documents = []indexEntry{}
	}
	body, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicWriteFile(s.indexPath(), body, 0o644)
}

func (s *Store) indexPath() string {
	return filepath.Join(s.root, "index.json")
}

func (s *Store) documentDir(key string) string {
	return filepath.Join(s.root, "files", key)
}

func (s *Store) metaPath(ref documentRef) string {
	return filepath.Join(s.documentDir(ref.Key), "meta.json")
}

func (s *Store) activityLogPath(ref documentRef) string {
	return filepath.Join(s.documentDir(ref.Key), "activity.ndjson")
}

func (s *Store) opLogPath(ref documentRef) string {
	return filepath.Join(s.documentDir(ref.Key), "ops.ndjson")
}

func (s *Store) documentLock(documentKey string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, ok := s.docLocks[documentKey]
	if !ok {
		lock = &sync.Mutex{}
		s.docLocks[documentKey] = lock
	}
	return lock
}

func (s *Store) lockDocument(documentKey string) func() {
	lock := s.documentLock(documentKey)
	lock.Lock()
	return lock.Unlock
}

func resolveMetadataRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, ".agentpad")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
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

func validateDocumentPath(path string) (string, os.FileInfo, error) {
	if path == "" {
		return "", nil, domain.NewError(domain.ErrCodeInvalidRequest, "missing path", 400)
	}
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		return "", nil, domain.NewError(domain.ErrCodeInvalidRequest, "path must be absolute", 400)
	}
	canonicalPath := cleanPath
	if resolved, err := filepath.EvalSymlinks(cleanPath); err == nil && resolved != "" {
		canonicalPath = resolved
	}
	info, err := os.Stat(canonicalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, domain.NewError(domain.ErrCodeDocumentNotFound, "document not found", 404)
		}
		return "", nil, err
	}
	if info.IsDir() {
		return "", nil, domain.NewError(domain.ErrCodeInvalidRequest, "path must point to a file", 400)
	}
	return canonicalPath, info, nil
}

func detectFormat(path string) domain.DocumentFormat {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return domain.DocumentFormatMarkdown
	case ".txt":
		return domain.DocumentFormatText
	case ".html", ".htm":
		return domain.DocumentFormatHTML
	case ".json":
		return domain.DocumentFormatJSON
	default:
		return domain.DocumentFormatCode
	}
}

func defaultTitle(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if name == "" {
		return filepath.Base(path)
	}
	return name
}

func shouldRefreshTitle(title, previousPath, currentPath string) bool {
	if previousPath == "" || previousPath == currentPath {
		return false
	}
	return title == defaultTitle(previousPath)
}

func sameIdentity(a, b fileIdentity) bool {
	return a.Available && b.Available && a.Device == b.Device && a.Inode == b.Inode
}

func normalizeSource(source string) string {
	return strings.ReplaceAll(source, "\r\n", "\n")
}

func touchIfMissing(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0o644)
}

func appendNDJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if _, err := file.Write(body); err != nil {
		return err
	}
	return file.Sync()
}

func readNDJSON[T any](path string) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	items := []T{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item T
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, scanner.Err()
}

func atomicWriteFile(path string, body []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func filePerm(path string) os.FileMode {
	info, err := os.Stat(path)
	if err != nil {
		return 0o644
	}
	return info.Mode().Perm()
}

func relevantBlocks(blocks []domain.Block, start, end int) []domain.Block {
	var result []domain.Block
	for _, block := range blocks {
		if block.End <= start || block.Start >= end {
			continue
		}
		result = append(result, block)
	}
	return result
}

func anchorForQuote(doc domain.Document, quote, prefix, suffix, blockID string) (*domain.Anchor, error) {
	if quote == "" {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest, "quote is required", 400)
	}
	if blockID != "" {
		for _, block := range doc.Blocks {
			if block.ID != blockID {
				continue
			}
			anchor, matches, err := findQuoteAnchorInRange(doc, quote, prefix, suffix, block.Start, block.End)
			if err != nil {
				return nil, err
			}
			if matches == 1 {
				return anchor, nil
			}
			break
		}
		if !blockExists(doc.Blocks, blockID) {
			return nil, domain.NewError(domain.ErrCodeInvalidRequest, "block not found", 404)
		}
	}
	anchor, matches, err := findQuoteAnchorInRange(doc, quote, prefix, suffix, 0, len([]rune(doc.Source)))
	if err != nil {
		return nil, err
	}
	if matches == 1 {
		return anchor, nil
	}
	if matches == 0 {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest, "quote not found", 404)
	}
	return nil, domain.NewError(domain.ErrCodeConflict, "quote match is ambiguous", 409)
}

func findQuoteAnchorInRange(doc domain.Document, quote, prefix, suffix string, start, end int) (*domain.Anchor, int, error) {
	sourceRunes := []rune(doc.Source)
	quoteRunes := []rune(quote)
	if len(quoteRunes) == 0 {
		return nil, 0, domain.NewError(domain.ErrCodeInvalidRequest, "quote is required", 400)
	}
	matches := 0
	var anchor *domain.Anchor
	for idx := start; idx+len(quoteRunes) <= end; idx++ {
		if string(sourceRunes[idx:idx+len(quoteRunes)]) != quote {
			continue
		}
		if prefix != "" && !strings.HasSuffix(string(sourceRunes[max(0, idx-len([]rune(prefix))):idx]), prefix) {
			continue
		}
		suffixEnd := min(len(sourceRunes), idx+len(quoteRunes)+len([]rune(suffix)))
		if suffix != "" && !strings.HasPrefix(string(sourceRunes[idx+len(quoteRunes):suffixEnd]), suffix) {
			continue
		}
		matches++
		if matches > 1 {
			return nil, matches, nil
		}
		nextAnchor, err := docmodel.AnchorFromSelection(doc, idx, idx+len(quoteRunes))
		if err != nil {
			return nil, 0, err
		}
		anchor = nextAnchor
	}
	return anchor, matches, nil
}

func blockExists(blocks []domain.Block, blockID string) bool {
	for _, block := range blocks {
		if block.ID == blockID {
			return true
		}
	}
	return false
}

func utf8Index(source string, byteIndex int) int {
	return len([]rune(source[:byteIndex]))
}

func threadIndex(items []domain.Thread, threadID string) int {
	for index, item := range items {
		if item.ID == threadID {
			return index
		}
	}
	return -1
}

func (s *Store) resolveAnchorStrictUnlocked(ref documentRef, doc domain.Document, anchor domain.Anchor) (domain.Anchor, error) {
	history, err := s.changesSinceUnlocked(ref, anchor.Revision)
	if err != nil {
		return domain.Anchor{}, err
	}
	return docmodel.ResolveAnchor(doc, anchor, history)
}

func (s *Store) resolveAnchorForDisplayUnlocked(ref documentRef, doc domain.Document, anchor domain.Anchor) (domain.Anchor, error) {
	resolved, err := s.resolveAnchorStrictUnlocked(ref, doc, anchor)
	resolved.Revision = doc.Revision
	if err != nil {
		return resolved, nil
	}
	return resolved, nil
}

func annotationIndex(items []domain.Annotation, annotationID string) int {
	for index, item := range items {
		if item.ID == annotationID {
			return index
		}
	}
	return -1
}

func suggestionIndex(items []domain.SuggestionBatch, batchID string) int {
	for index, item := range items {
		if item.ID == batchID {
			return index
		}
	}
	return -1
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *Store) String() string {
	return fmt.Sprintf("workspace-store(%p,%s)", s, s.root)
}
