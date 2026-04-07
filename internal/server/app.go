package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	pathpkg "path"
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
	store     store.Store
	hub       *Hub
	staticDir string
	staticFS  fs.FS
}

func New(st store.Store, staticDir string) *App {
	return &App{
		store:     st,
		hub:       NewHub(st),
		staticDir: staticDir,
	}
}

func NewWithStaticFS(st store.Store, staticFS fs.FS) *App {
	app := New(st, "")
	app.staticFS = staticFS
	return app
}

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", a.handleHealth)
	mux.HandleFunc("POST /api/documents/import", a.handleImportDocument)
	mux.HandleFunc("POST /api/documents", a.handleCreateDocument)
	mux.HandleFunc("GET /api/documents/{id}", a.handleGetDocument)
	mux.HandleFunc("GET /api/documents/{id}/read", a.handleReadDocument)
	mux.HandleFunc("POST /api/documents/{id}/edit", a.handleEditDocument)
	mux.HandleFunc("GET /api/documents/{id}/threads", a.handleListThreads)
	mux.HandleFunc("GET /api/documents/{id}/thread", a.handleGetThread)
	mux.HandleFunc("POST /api/documents/{id}/threads", a.handleCreateThread)
	mux.HandleFunc("POST /api/documents/{id}/threads/{thread_id}/replies", a.handleReplyThreadByPath)
	mux.HandleFunc("POST /api/documents/{id}/threads/{thread_id}/resolve", a.handleResolveThreadByPath)
	mux.HandleFunc("POST /api/documents/{id}/threads/{thread_id}/reopen", a.handleReopenThreadByPath)
	mux.HandleFunc("POST /api/documents/{id}/thread-replies", a.handleReplyThread)
	mux.HandleFunc("POST /api/documents/{id}/thread-reanchor", a.handleReanchorThread)
	mux.HandleFunc("POST /api/documents/{id}/thread-resolve", a.handleResolveThread)
	mux.HandleFunc("POST /api/documents/{id}/thread-reopen", a.handleReopenThread)
	mux.HandleFunc("GET /api/documents/{id}/activity", a.handleActivity)
	mux.HandleFunc("GET /api/documents/{id}/export", a.handleExportDocument)
	mux.HandleFunc("GET /api/documents/{id}/live", a.hub.HandleLive)

	return withCORS(withLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			mux.ServeHTTP(w, r)
			return
		}
		a.serveStatic(w, r)
	})))
}

func (a *App) serveStatic(w http.ResponseWriter, r *http.Request) {
	if a.staticDir == "" && a.staticFS == nil {
		http.NotFound(w, r)
		return
	}
	if a.staticDir == "" {
		a.serveStaticFS(w, r)
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

func (a *App) serveStaticFS(w http.ResponseWriter, r *http.Request) {
	staticPath := strings.TrimPrefix(pathpkg.Clean("/"+r.URL.Path), "/")
	if staticPath == "" {
		staticPath = "index.html"
	}
	info, err := fs.Stat(a.staticFS, staticPath)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && info.IsDir()) {
		staticPath = "index.html"
	}
	http.ServeFileFS(w, r, a.staticFS, staticPath)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleImportDocument(w http.ResponseWriter, r *http.Request) {
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, "file upload is required", 400))
		return
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		writeError(w, err)
		return
	}
	imported, err := importexport.ImportData(header.Filename, body, header.Filename)
	if err != nil {
		writeError(w, err)
		return
	}
	doc, err := a.store.ImportDocument(r.Context(), imported, actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, doc)
}

func (a *App) handleCreateDocument(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title  string                `json:"title"`
		Format domain.DocumentFormat `json:"format"`
		Source string                `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, err.Error(), 400))
		return
	}
	doc, err := a.store.CreateDocument(r.Context(), req.Title, req.Format, req.Source, actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, doc)
}

func (a *App) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	doc, err := a.store.GetDocument(r.Context(), documentIDFromRequest(r), actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.Query().Get("summary") == "true" {
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

func (a *App) handleReadDocument(w http.ResponseWriter, r *http.Request) {
	occurrence := parseIntDefault(r.URL.Query().Get("occurrence"), 0)
	read, err := a.store.ReadDocument(r.Context(), documentIDFromRequest(r), actorFromRequest(r), store.ReadOptions{
		Full:       r.URL.Query().Get("full") == "true",
		Match:      r.URL.Query().Get("match"),
		Before:     r.URL.Query().Get("before"),
		After:      r.URL.Query().Get("after"),
		Occurrence: occurrence,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, read)
}

func (a *App) handleEditDocument(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ThreadID   string `json:"thread_id"`
		Replace    string `json:"replace"`
		Match      string `json:"match"`
		Before     string `json:"before"`
		After      string `json:"after"`
		Occurrence int    `json:"occurrence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, err.Error(), 400))
		return
	}
	var (
		doc       domain.Document
		canonical collab.Op
		thread    *domain.Thread
		err       error
	)
	documentID := documentIDFromRequest(r)
	if req.ThreadID != "" {
		var updatedThread domain.Thread
		updatedThread, doc, canonical, err = a.store.ApplyThreadEdit(r.Context(), documentID, req.ThreadID, req.Replace, actorFromRequest(r))
		thread = &updatedThread
	} else {
		doc, canonical, err = a.store.ApplyMatchEdit(r.Context(), documentID, domain.MatchSelector{
			Match:      req.Match,
			Before:     req.Before,
			After:      req.After,
			Occurrence: req.Occurrence,
		}, req.Replace, actorFromRequest(r))
	}
	if err != nil {
		writeError(w, err)
		return
	}
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
	items, err := a.store.ListThreads(r.Context(), documentIDFromRequest(r), actorFromRequest(r))
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
	thread, err := a.store.GetThread(r.Context(), documentIDFromRequest(r), r.URL.Query().Get("thread_id"), actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, thread)
}

func (a *App) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Body       string `json:"body"`
		Start      int    `json:"start"`
		End        int    `json:"end"`
		Match      string `json:"match"`
		Before     string `json:"before"`
		After      string `json:"after"`
		Occurrence int    `json:"occurrence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, err.Error(), 400))
		return
	}
	documentID := documentIDFromRequest(r)
	var (
		thread domain.Thread
		err    error
	)
	switch {
	case strings.TrimSpace(req.Match) != "":
		thread, err = a.store.CreateThreadByMatch(r.Context(), documentID, domain.MatchSelector{
			Match:      req.Match,
			Before:     req.Before,
			After:      req.After,
			Occurrence: req.Occurrence,
		}, req.Body, actorFromRequest(r))
	default:
		doc, docErr := a.store.GetDocument(r.Context(), documentID, actorFromRequest(r))
		if docErr != nil {
			writeError(w, docErr)
			return
		}
		anchor, anchorErr := docmodel.AnchorFromSelection(doc, req.Start, req.End)
		if anchorErr != nil {
			writeError(w, anchorErr)
			return
		}
		thread, err = a.store.CreateThread(r.Context(), documentID, *anchor, req.Body, actorFromRequest(r))
	}
	if err != nil {
		writeError(w, err)
		return
	}
	a.hub.NotifyDocument(thread.DocumentID, "threads", map[string]any{"thread_id": thread.ID})
	writeJSON(w, http.StatusCreated, thread)
}

func (a *App) handleReplyThread(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ThreadID string `json:"thread_id"`
		Body     string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, err.Error(), 400))
		return
	}
	thread, comment, err := a.store.ReplyThread(r.Context(), documentIDFromRequest(r), req.ThreadID, req.Body, actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	a.hub.NotifyDocument(thread.DocumentID, "threads", map[string]any{"thread_id": thread.ID, "comment_id": comment.ID})
	writeJSON(w, http.StatusCreated, map[string]any{"thread": thread, "comment": comment})
}

func (a *App) handleReplyThreadByPath(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, err.Error(), 400))
		return
	}
	thread, comment, err := a.store.ReplyThread(r.Context(), documentIDFromRequest(r), r.PathValue("thread_id"), req.Body, actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	a.hub.NotifyDocument(thread.DocumentID, "threads", map[string]any{"thread_id": thread.ID, "comment_id": comment.ID})
	writeJSON(w, http.StatusCreated, map[string]any{"thread": thread, "comment": comment})
}

func (a *App) handleReanchorThread(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ThreadID   string `json:"thread_id"`
		Match      string `json:"match"`
		Before     string `json:"before"`
		After      string `json:"after"`
		Occurrence int    `json:"occurrence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, err.Error(), 400))
		return
	}
	thread, err := a.store.ReanchorThreadByMatch(r.Context(), documentIDFromRequest(r), req.ThreadID, domain.MatchSelector{
		Match:      req.Match,
		Before:     req.Before,
		After:      req.After,
		Occurrence: req.Occurrence,
	}, actorFromRequest(r))
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

func (a *App) handleResolveThreadByPath(w http.ResponseWriter, r *http.Request) {
	a.handleThreadStatusByPath(w, r, domain.ThreadStatusResolved)
}

func (a *App) handleReopenThreadByPath(w http.ResponseWriter, r *http.Request) {
	a.handleThreadStatusByPath(w, r, domain.ThreadStatusOpen)
}

func (a *App) handleThreadStatus(w http.ResponseWriter, r *http.Request, status domain.ThreadStatus) {
	var req struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, err.Error(), 400))
		return
	}
	thread, err := a.store.SetThreadStatus(r.Context(), documentIDFromRequest(r), req.ThreadID, status, actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	a.hub.NotifyDocument(thread.DocumentID, "threads", map[string]any{"thread_id": thread.ID})
	writeJSON(w, http.StatusOK, thread)
}

func (a *App) handleThreadStatusByPath(w http.ResponseWriter, r *http.Request, status domain.ThreadStatus) {
	thread, err := a.store.SetThreadStatus(r.Context(), documentIDFromRequest(r), r.PathValue("thread_id"), status, actorFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	a.hub.NotifyDocument(thread.DocumentID, "threads", map[string]any{"thread_id": thread.ID})
	writeJSON(w, http.StatusOK, thread)
}

func (a *App) handleActivity(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (a *App) handleExportDocument(w http.ResponseWriter, r *http.Request) {
	doc, err := a.store.GetDocument(r.Context(), documentIDFromRequest(r), actorFromRequest(r))
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
	filename := sanitizeFilename(doc.Title) + "." + ext
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Content-Type", contentTypeForFormat(format))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
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

func documentIDFromRequest(r *http.Request) string {
	return r.PathValue("id")
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
