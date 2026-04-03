package server

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cyrusaf/agentpad/internal/collab"
	"github.com/cyrusaf/agentpad/internal/docmodel"
	"github.com/cyrusaf/agentpad/internal/domain"
	"github.com/cyrusaf/agentpad/internal/importexport"
	"github.com/cyrusaf/agentpad/internal/store"
)

type App struct {
	store     *store.Store
	hub       *Hub
	staticDir string
}

func New(store *store.Store, staticDir string) *App {
	return &App{
		store:     store,
		hub:       NewHub(store),
		staticDir: staticDir,
	}
}

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", a.handleHealth)
	mux.HandleFunc("GET /api/files/open", a.handleOpenFile)
	mux.HandleFunc("GET /api/files/read", a.handleReadFile)
	mux.HandleFunc("POST /api/files/edit", a.handleEditFile)
	mux.HandleFunc("GET /api/files/threads", a.handleListThreads)
	mux.HandleFunc("GET /api/files/thread", a.handleGetThread)
	mux.HandleFunc("POST /api/files/threads", a.handleCreateThread)
	mux.HandleFunc("POST /api/files/thread-replies", a.handleReplyThread)
	mux.HandleFunc("POST /api/files/thread-reanchor", a.handleReanchorThread)
	mux.HandleFunc("POST /api/files/thread-resolve", a.handleResolveThread)
	mux.HandleFunc("POST /api/files/thread-reopen", a.handleReopenThread)
	mux.HandleFunc("GET /api/files/activity", a.handleActivity)
	mux.HandleFunc("GET /api/files/export", a.handleExportFile)
	mux.HandleFunc("GET /api/files/live", a.hub.HandleLive)

	return withCORS(withLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			mux.ServeHTTP(w, r)
			return
		}
		a.serveStatic(w, r)
	})))
}

func (a *App) serveStatic(w http.ResponseWriter, r *http.Request) {
	if a.staticDir == "" {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(a.staticDir, strings.TrimPrefix(r.URL.Path, "/"))
	if r.URL.Path == "/" {
		path = filepath.Join(a.staticDir, "index.html")
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && info.IsDir()) {
		path = filepath.Join(a.staticDir, "index.html")
	}
	http.ServeFile(w, r, path)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleOpenFile(w http.ResponseWriter, r *http.Request) {
	doc, err := a.store.OpenDocument(r.Context(), requiredPath(r), actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.Query().Get("full") == "false" {
		writeJSON(w, http.StatusOK, domain.DocumentSummary{
			ID:        doc.ID,
			Title:     doc.Title,
			Format:    doc.Format,
			Revision:  doc.Revision,
			UpdatedAt: doc.UpdatedAt,
		})
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (a *App) handleReadFile(w http.ResponseWriter, r *http.Request) {
	start := parseIntDefault(r.URL.Query().Get("start"), -1)
	end := parseIntDefault(r.URL.Query().Get("end"), -1)
	read, err := a.store.ReadDocument(r.Context(), requiredPath(r), actorFromRequest(r), store.ReadOptions{
		Full:    r.URL.Query().Get("full") != "false",
		BlockID: r.URL.Query().Get("block_id"),
		Start:   start,
		End:     end,
		Query:   r.URL.Query().Get("query"),
		Quote:   r.URL.Query().Get("quote"),
		Prefix:  r.URL.Query().Get("prefix"),
		Suffix:  r.URL.Query().Get("suffix"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, read)
}

func (a *App) handleEditFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path         string         `json:"path"`
		Position     int            `json:"position"`
		DeleteCount  int            `json:"delete_count"`
		InsertText   string         `json:"insert_text"`
		BaseRevision int64          `json:"base_revision"`
		Anchor       *domain.Anchor `json:"anchor"`
		ThreadID     string         `json:"thread_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, err.Error(), 400))
		return
	}
	if req.ThreadID != "" && (req.Anchor != nil || req.BaseRevision != 0 || req.DeleteCount != 0 || req.Position != 0) {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, "thread edits cannot be combined with anchor or positional edit fields", 400))
		return
	}
	var (
		actor     = actorFromRequest(r)
		doc       domain.Document
		canonical collab.Op
		thread    *domain.Thread
		err       error
	)
	if req.ThreadID != "" {
		var updatedThread domain.Thread
		updatedThread, doc, canonical, err = a.store.ApplyThreadEdit(r.Context(), req.Path, req.ThreadID, req.InsertText, actor)
		thread = &updatedThread
	} else if req.Anchor != nil {
		doc, canonical, err = a.store.ApplyAnchorEdit(r.Context(), req.Path, *req.Anchor, req.InsertText, actor)
	} else {
		doc, canonical, err = a.store.ApplyOp(r.Context(), req.Path, collab.Op{
			Position:     req.Position,
			DeleteCount:  req.DeleteCount,
			InsertText:   req.InsertText,
			BaseRevision: req.BaseRevision,
			Author:       actor,
		}, actor)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	canonical.Author = actor
	a.hub.NotifyOpApplied(doc.ID, doc.Revision, canonical)
	a.hub.NotifyDocument(doc.ID, "threads", map[string]any{
		"revision":  doc.Revision,
		"thread_id": req.ThreadID,
	})
	response := map[string]any{"document": doc, "op": canonical}
	if thread != nil {
		response["thread"] = *thread
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleListThreads(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListThreads(r.Context(), requiredPath(r), actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.Query().Get("summary") == "true" {
		summaries := make([]domain.ThreadSummary, 0, len(items))
		for _, item := range items {
			summaries = append(summaries, summarizeThread(item))
		}
		writeJSON(w, http.StatusOK, summaries)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *App) handleGetThread(w http.ResponseWriter, r *http.Request) {
	thread, err := a.store.GetThread(r.Context(), requiredPath(r), r.URL.Query().Get("thread_id"), actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, thread)
}

func (a *App) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string         `json:"path"`
		Body   string         `json:"body"`
		Start  int            `json:"start"`
		End    int            `json:"end"`
		Anchor *domain.Anchor `json:"anchor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, err.Error(), 400))
		return
	}
	doc, err := a.store.GetDocument(r.Context(), req.Path, actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	anchor, err := a.anchorFromRequest(doc, req.Start, req.End, req.Anchor)
	if err != nil {
		writeError(w, err)
		return
	}
	thread, err := a.store.CreateThread(r.Context(), doc.ID, *anchor, req.Body, actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	a.hub.NotifyDocument(doc.ID, "threads", map[string]any{"thread_id": thread.ID})
	writeJSON(w, http.StatusCreated, thread)
}

func (a *App) handleReplyThread(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path     string `json:"path"`
		ThreadID string `json:"thread_id"`
		Body     string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, err.Error(), 400))
		return
	}
	thread, comment, err := a.store.ReplyThread(r.Context(), req.Path, req.ThreadID, req.Body, actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	a.hub.NotifyDocument(thread.DocumentID, "threads", map[string]any{"thread_id": thread.ID, "comment_id": comment.ID})
	writeJSON(w, http.StatusCreated, map[string]any{"thread": thread, "comment": comment})
}

func (a *App) handleReanchorThread(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path     string         `json:"path"`
		ThreadID string         `json:"thread_id"`
		Start    int            `json:"start"`
		End      int            `json:"end"`
		Anchor   *domain.Anchor `json:"anchor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, err.Error(), 400))
		return
	}
	doc, err := a.store.GetDocument(r.Context(), req.Path, actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	anchor, err := a.anchorFromRequest(doc, req.Start, req.End, req.Anchor)
	if err != nil {
		writeError(w, err)
		return
	}
	thread, err := a.store.ReanchorThread(r.Context(), req.Path, req.ThreadID, *anchor, actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	a.hub.NotifyDocument(thread.DocumentID, "threads", map[string]any{"thread_id": thread.ID})
	writeJSON(w, http.StatusOK, thread)
}

func (a *App) handleResolveThread(w http.ResponseWriter, r *http.Request) {
	a.handleThreadStatus(w, r, domain.ThreadStatusResolved)
}

func (a *App) handleReopenThread(w http.ResponseWriter, r *http.Request) {
	a.handleThreadStatus(w, r, domain.ThreadStatusOpen)
}

func (a *App) handleThreadStatus(w http.ResponseWriter, r *http.Request, status domain.ThreadStatus) {
	var req struct {
		Path     string `json:"path"`
		ThreadID string `json:"thread_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, err.Error(), 400))
		return
	}
	thread, err := a.store.SetThreadStatus(r.Context(), req.Path, req.ThreadID, status, actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	a.hub.NotifyDocument(thread.DocumentID, "threads", map[string]any{"thread_id": thread.ID})
	writeJSON(w, http.StatusOK, thread)
}

func (a *App) handleActivity(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.Activity(r.Context(), requiredPath(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *App) handleExportFile(w http.ResponseWriter, r *http.Request) {
	doc, err := a.store.GetDocument(r.Context(), requiredPath(r), actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	format := domain.DocumentFormat(r.URL.Query().Get("format"))
	if format == "" {
		format = domain.DocumentFormatMarkdown
	}
	body, ext, err := importexport.ExportDocument(doc, format)
	if err != nil {
		writeError(w, err)
		return
	}
	_ = a.store.RecordActivity(context.Background(), doc.ID, "document.exported", actorFromRequest(r), map[string]any{"format": format})
	filename := sanitizeFilename(doc.Title) + "." + ext
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Content-Type", contentTypeForFormat(format))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (a *App) anchorFromRequest(doc domain.Document, start, end int, provided *domain.Anchor) (*domain.Anchor, error) {
	if provided != nil {
		return provided, nil
	}
	return docmodel.AnchorFromSelection(doc, start, end)
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		_ = start
	})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-AgentPad-Actor")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	appErr := domain.AsError(err)
	writeJSON(w, appErr.StatusCode, appErr)
}

func actorFromRequest(r *http.Request) string {
	if actor := r.Header.Get("X-AgentPad-Actor"); actor != "" {
		return actor
	}
	if actor := r.URL.Query().Get("actor"); actor != "" {
		return actor
	}
	return "agentpad-user"
}

func requiredPath(r *http.Request) string {
	return r.URL.Query().Get("path")
}

func parseIntDefault(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func sanitizeFilename(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "document"
	}
	value = strings.ReplaceAll(value, " ", "-")
	value = strings.ToLower(value)
	return value
}

func contentTypeForFormat(format domain.DocumentFormat) string {
	switch format {
	case domain.DocumentFormatHTML:
		return "text/html; charset=utf-8"
	case domain.DocumentFormatJSON:
		return "application/json"
	default:
		return "text/plain; charset=utf-8"
	}
}

func summarizeThread(thread domain.Thread) domain.ThreadSummary {
	summary := domain.ThreadSummary{
		ID:           thread.ID,
		DocumentID:   thread.DocumentID,
		Anchor:       thread.Anchor,
		Status:       thread.Status,
		Author:       thread.Author,
		CreatedAt:    thread.CreatedAt,
		UpdatedAt:    thread.UpdatedAt,
		CommentCount: len(thread.Comments),
	}
	if count := len(thread.Comments); count > 0 {
		last := thread.Comments[count-1]
		summary.LastCommentID = last.ID
		summary.LastCommentBy = last.Author
		summary.LastCommentAt = &last.CreatedAt
	}
	return summary
}
