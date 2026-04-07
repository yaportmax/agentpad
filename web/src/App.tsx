import { startTransition, useEffect, useEffectEvent, useRef, useState } from "react";
import type { CSSProperties, ChangeEvent, FormEvent, PointerEvent as ReactPointerEvent } from "react";

import { EditorPane, type EditorPaneHandle } from "./components/EditorPane";
import { api } from "./lib/api";
import { clearUnreadThreadState, diffUnreadThreadActivity } from "./lib/threadHighlights";
import type { Document, Presence, SelectionRange, Thread } from "./lib/types";

interface RouteState {
  documentId: string | null;
  threadId: string | null;
}

type CommentsView = "open" | "resolved";

const COMMENTS_WIDTH_STORAGE_KEY = "agentpad.commentsWidth";
const DEFAULT_COMMENTS_WIDTH = 560;
const MIN_COMMENTS_WIDTH = 360;
const MAX_COMMENTS_WIDTH = 960;
const MIN_EDITOR_WIDTH = 420;

function readRoute(): RouteState {
  const url = new URL(window.location.href);
  const documentId = url.pathname.replace(/^\/+|\/+$/g, "");
  return {
    documentId: documentId ? decodeURIComponent(documentId) : null,
    threadId: url.searchParams.get("thread"),
  };
}

function writeRoute(route: RouteState, mode: "push" | "replace" = "push") {
  const url = new URL(window.location.href);
  url.pathname = route.documentId ? `/${encodeURIComponent(route.documentId)}` : "/";
  if (route.documentId && route.threadId) {
    url.searchParams.set("thread", route.threadId);
  } else {
    url.searchParams.delete("thread");
  }
  const next = `${url.pathname}${url.search}${url.hash}`;
  if (mode === "replace") {
    window.history.replaceState({}, "", next);
  } else {
    window.history.pushState({}, "", next);
  }
}

function formatTimestamp(value: string) {
  return new Date(value).toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

function sortThreadsByUpdatedAt(left: Thread, right: Thread) {
  return right.updated_at.localeCompare(left.updated_at);
}

function formatQuote(thread: Thread) {
  const quote = thread.anchor.quote.replace(/\s+/g, " ").trim();
  if (!thread.anchor.resolved) {
    if (!quote) {
      return "Commented text changed after edits";
    }
    const label = `Changed after edits: ${quote}`;
    return label.length > 88 ? `${label.slice(0, 85)}...` : label;
  }
  if (!quote) {
    return "Commented text";
  }
  return quote.length > 88 ? `${quote.slice(0, 85)}...` : quote;
}

function threadQuoteTitle(thread: Thread) {
  const quote = thread.anchor.quote.trim();
  if (!thread.anchor.resolved) {
    if (!quote) {
      return "Commented text changed after edits. Highlight hidden until it is re-anchored.";
    }
    return `${quote}\n\nCommented text changed after edits. Highlight hidden until it is re-anchored.`;
  }
  return thread.anchor.quote;
}

function getSelectionPreview(selection: SelectionRange | null) {
  if (!selection) {
    return "";
  }
  const text = selection.text.replace(/\s+/g, " ").trim();
  if (!text) {
    return "";
  }
  return text.length > 96 ? `${text.slice(0, 93)}...` : text;
}

function getComposerStyle(selection: SelectionRange | null): CSSProperties | undefined {
  if (!selection?.rect) {
    return { left: 24, top: 96 };
  }
  const width = 320;
  const left = Math.max(16, Math.min(selection.rect.left, window.innerWidth - width - 16));
  const top = Math.max(80, Math.min(selection.rect.bottom + 12, window.innerHeight - 240));
  return { left, top };
}

function clampNumber(value: number, min: number, max: number) {
  return Math.min(Math.max(value, min), max);
}

function getCommentsWidthMax(viewportWidth: number) {
  return Math.max(MIN_COMMENTS_WIDTH, Math.min(MAX_COMMENTS_WIDTH, viewportWidth - MIN_EDITOR_WIDTH));
}

function clampCommentsWidth(width: number, viewportWidth = window.innerWidth) {
  return clampNumber(Math.round(width), MIN_COMMENTS_WIDTH, getCommentsWidthMax(viewportWidth));
}

function readStoredCommentsWidth() {
  const raw = localStorage.getItem(COMMENTS_WIDTH_STORAGE_KEY);
  if (!raw) {
    return clampCommentsWidth(DEFAULT_COMMENTS_WIDTH);
  }
  const parsed = Number.parseInt(raw, 10);
  if (!Number.isFinite(parsed)) {
    return clampCommentsWidth(DEFAULT_COMMENTS_WIDTH);
  }
  return clampCommentsWidth(parsed);
}

export default function App() {
  const [route, setRoute] = useState<RouteState>(() => readRoute());
  const [actor, setActor] = useState(localStorage.getItem("agentpad.actor") ?? "browser-user");
  const [currentDoc, setCurrentDoc] = useState<Document | null>(null);
  const [selection, setSelection] = useState<SelectionRange | null>(null);
  const [presence, setPresence] = useState<Presence[]>([]);
  const [threads, setThreads] = useState<Thread[]>([]);
  const [unreadThreadIds, setUnreadThreadIds] = useState<Set<string>>(() => new Set());
  const [unreadCommentIds, setUnreadCommentIds] = useState<Set<string>>(() => new Set());
  const [status, setStatus] = useState("Create or import a document to begin.");
  const [commentBody, setCommentBody] = useState("");
  const [replyDrafts, setReplyDrafts] = useState<Record<string, string>>({});
  const [commentsWidth, setCommentsWidth] = useState(() => readStoredCommentsWidth());
  const [commentsCollapsed, setCommentsCollapsed] = useState(false);
  const [commentsView, setCommentsView] = useState<CommentsView>("open");
  const [createTitle, setCreateTitle] = useState("");
  const [createSource, setCreateSource] = useState("");
  const [importFile, setImportFile] = useState<File | null>(null);
  const [isDocumentActionPending, setIsDocumentActionPending] = useState(false);
  const editorRef = useRef<EditorPaneHandle | null>(null);
  const importInputRef = useRef<HTMLInputElement | null>(null);
  const lastFocusedThreadRef = useRef<string | null>(null);
  const previousThreadsRef = useRef<Thread[]>([]);
  const threadRefreshTimeoutRef = useRef<number | null>(null);
  const resizeStateRef = useRef<{ startX: number; startWidth: number } | null>(null);
  const activeThreadId = route.threadId;
  const openThreads = threads.filter((thread) => thread.status === "open").sort(sortThreadsByUpdatedAt);
  const resolvedThreads = threads.filter((thread) => thread.status === "resolved").sort(sortThreadsByUpdatedAt);
  const visibleThreads = commentsView === "open" ? openThreads : resolvedThreads;
  const activeThread = threads.find((thread) => thread.id === activeThreadId) ?? null;
  const activeVisibleThread = visibleThreads.find((thread) => thread.id === activeThreadId) ?? null;
  const highlightThreads =
    commentsView === "open"
      ? openThreads
      : activeThread && activeThread.status === "resolved"
        ? [activeThread]
        : [];
  const selectionPreview = getSelectionPreview(selection);
  const composerStyle = getComposerStyle(selection);

  const handleRevisionChange = useEffectEvent((revision: number, source: string) => {
    setCurrentDoc((current) => (current ? { ...current, revision, source } : current));
  });

  const navigateTo = useEffectEvent((nextRoute: RouteState, mode: "push" | "replace" = "push") => {
    writeRoute(nextRoute, mode);
    setRoute(nextRoute);
  });

  const presentDocument = useEffectEvent((document: Document, nextStatus: string) => {
    startTransition(() => {
      setCurrentDoc(document);
      setPresence([]);
      setThreads([]);
      setSelection(null);
      setCommentBody("");
      setReplyDrafts({});
      setStatus(nextStatus);
    });
  });

  const refreshThreads = useEffectEvent(async (documentID: string, mode: "load" | "local" | "live" = "local") => {
    const nextThreads = await api.listThreads(documentID);
    const previousThreads = previousThreadsRef.current;
    previousThreadsRef.current = nextThreads ?? [];
    const unreadDiff =
      mode === "live" ? diffUnreadThreadActivity(previousThreads, nextThreads ?? [], actor, route.threadId) : null;
    startTransition(() => {
      setThreads(nextThreads ?? []);
      if (mode === "load") {
        setUnreadThreadIds(new Set());
        setUnreadCommentIds(new Set());
        return;
      }
      if (!unreadDiff) {
        return;
      }
      if (unreadDiff.threadIds.length > 0) {
        setUnreadThreadIds((current) => {
          const next = new Set(current);
          for (const threadID of unreadDiff.threadIds) {
            next.add(threadID);
          }
          return next;
        });
      }
      if (unreadDiff.commentIds.length > 0) {
        setUnreadCommentIds((current) => {
          const next = new Set(current);
          for (const commentID of unreadDiff.commentIds) {
            next.add(commentID);
          }
          return next;
        });
      }
    });
  });

  const clearThreadUnread = useEffectEvent((threadID: string, sourceThreads: Thread[] = threads) => {
    const nextUnreadState = clearUnreadThreadState(threadID, sourceThreads, {
      threadIds: unreadThreadIds,
      commentIds: unreadCommentIds,
    });
    if (nextUnreadState.threadIds !== unreadThreadIds) {
      setUnreadThreadIds(new Set(nextUnreadState.threadIds));
    }
    if (nextUnreadState.commentIds !== unreadCommentIds) {
      setUnreadCommentIds(new Set(nextUnreadState.commentIds));
    }
  });

  const loadDocument = useEffectEvent(async (documentID: string) => {
    const document = await api.getDocument(documentID);
    presentDocument(document, `Opened ${document.title}`);
    await refreshThreads(document.id, "load");
  });

  const openDocument = useEffectEvent(async (document: Document, nextStatus: string, mode: "push" | "replace" = "push") => {
    navigateTo({ documentId: document.id, threadId: null }, mode);
    presentDocument(document, nextStatus);
    await refreshThreads(document.id, "load");
  });

  const handleDocumentArtifactHint = useEffectEvent(() => {
    if (currentDoc) {
      void loadDocument(currentDoc.id);
    }
  });

  const handleThreadsArtifactHint = useEffectEvent(() => {
    if (currentDoc) {
      if (threadRefreshTimeoutRef.current !== null) {
        window.clearTimeout(threadRefreshTimeoutRef.current);
      }
      threadRefreshTimeoutRef.current = window.setTimeout(() => {
        threadRefreshTimeoutRef.current = null;
        void refreshThreads(currentDoc.id, "live");
      }, 120);
    }
  });

  useEffect(() => {
    localStorage.setItem("agentpad.actor", actor);
  }, [actor]);

  useEffect(() => {
    localStorage.setItem(COMMENTS_WIDTH_STORAGE_KEY, String(commentsWidth));
  }, [commentsWidth]);

  useEffect(() => {
    const handlePopState = () => {
      setRoute(readRoute());
    };
    window.addEventListener("popstate", handlePopState);
    return () => {
      window.removeEventListener("popstate", handlePopState);
    };
  }, []);

  useEffect(() => {
    return () => {
      if (threadRefreshTimeoutRef.current !== null) {
        window.clearTimeout(threadRefreshTimeoutRef.current);
      }
    };
  }, []);

  useEffect(() => {
    const handleResize = () => {
      setCommentsWidth((current: number) => clampCommentsWidth(current));
    };
    window.addEventListener("resize", handleResize);
    return () => {
      window.removeEventListener("resize", handleResize);
    };
  }, []);

  useEffect(() => {
    const handlePointerMove = (event: PointerEvent) => {
      if (!resizeStateRef.current) {
        return;
      }
      const delta = resizeStateRef.current.startX - event.clientX;
      setCommentsWidth(clampCommentsWidth(resizeStateRef.current.startWidth + delta));
    };

    const stopResize = () => {
      if (!resizeStateRef.current) {
        return;
      }
      resizeStateRef.current = null;
      document.body.classList.remove("is-resizing-comments");
    };

    window.addEventListener("pointermove", handlePointerMove);
    window.addEventListener("pointerup", stopResize);
    window.addEventListener("pointercancel", stopResize);
    return () => {
      window.removeEventListener("pointermove", handlePointerMove);
      window.removeEventListener("pointerup", stopResize);
      window.removeEventListener("pointercancel", stopResize);
      document.body.classList.remove("is-resizing-comments");
    };
  }, []);

  useEffect(() => {
    lastFocusedThreadRef.current = null;
    previousThreadsRef.current = [];
    setUnreadThreadIds(new Set());
    setUnreadCommentIds(new Set());
    setCommentsView("open");
  }, [route.documentId]);

  useEffect(() => {
    if (!activeThread) {
      return;
    }
    const nextView = activeThread.status === "resolved" ? "resolved" : "open";
    setCommentsView((current) => (current === nextView ? current : nextView));
  }, [activeThread]);

  useEffect(() => {
    const documentID = route.documentId;
    if (!documentID) {
      startTransition(() => {
        setCurrentDoc(null);
        setSelection(null);
        setPresence([]);
        setThreads([]);
        setUnreadThreadIds(new Set());
        setUnreadCommentIds(new Set());
        setCommentBody("");
        setReplyDrafts({});
        setStatus("Create or import a document to begin.");
      });
      return;
    }
    if (currentDoc?.id === documentID) {
      return;
    }
    startTransition(() => {
      setCurrentDoc(null);
      setThreads([]);
      setPresence([]);
      setSelection(null);
      setUnreadThreadIds(new Set());
      setUnreadCommentIds(new Set());
      setCommentBody("");
      setReplyDrafts({});
    });
    void (async () => {
      try {
        await loadDocument(documentID);
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unable to open that document.";
        setStatus(message);
        navigateTo({ documentId: null, threadId: null }, "replace");
      }
    })();
  }, [route.documentId, currentDoc?.id]);

  useEffect(() => {
    if (!activeThreadId) {
      return;
    }
    const activeCard = window.document.querySelector<HTMLElement>(`[data-thread-card="${activeThreadId}"]`);
    activeCard?.scrollIntoView({ block: "nearest" });
  }, [activeThreadId]);

  useEffect(() => {
    if (!activeThreadId) {
      return;
    }
    clearThreadUnread(activeThreadId, threads);
  }, [activeThreadId, threads]);

  useEffect(() => {
    if (!activeThreadId || activeThreadId === lastFocusedThreadRef.current) {
      return;
    }
    const thread = threads.find((item) => item.id === activeThreadId);
    if (!thread) {
      return;
    }
    if (!thread.anchor.resolved) {
      setStatus("Commented text changed after edits. Review the thread before relying on its old highlight.");
      lastFocusedThreadRef.current = activeThreadId;
      return;
    }
    editorRef.current?.focusRange(thread.anchor.doc_start, thread.anchor.doc_end);
    lastFocusedThreadRef.current = activeThreadId;
  }, [activeThreadId, threads]);

  async function handleCreateSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setIsDocumentActionPending(true);
    try {
      const document = await api.createDocument({
        title: createTitle.trim() || "Untitled",
        source: createSource,
        format: "markdown",
      });
      setCreateTitle("");
      setCreateSource("");
      setImportFile(null);
      if (importInputRef.current) {
        importInputRef.current.value = "";
      }
      await openDocument(document, `Created ${document.title}`);
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Could not create the document.");
    } finally {
      setIsDocumentActionPending(false);
    }
  }

  function handleImportFileChange(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0] ?? null;
    setImportFile(file);
    if (file) {
      setStatus(`Ready to import ${file.name}`);
    }
  }

  async function handleImportSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!importFile) {
      setStatus("Choose a file to import.");
      return;
    }
    setIsDocumentActionPending(true);
    try {
      const document = await api.importDocument(importFile);
      setImportFile(null);
      if (importInputRef.current) {
        importInputRef.current.value = "";
      }
      await openDocument(document, `Imported ${document.title}`);
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Could not import that file.");
    } finally {
      setIsDocumentActionPending(false);
    }
  }

  async function createThread() {
    if (!currentDoc || !selection) {
      setStatus("Select some text first.");
      return;
    }
    if (!commentBody.trim()) {
      setStatus("Add a comment first.");
      return;
    }
    const created = await api.createThread(currentDoc.id, {
      body: commentBody,
      start: selection.start,
      end: selection.end,
    });
    setCommentBody("");
    setSelection(null);
    editorRef.current?.clearSelection();
    await refreshThreads(currentDoc.id, "local");
    navigateTo({ documentId: currentDoc.id, threadId: created.id }, "replace");
    setStatus("Comment added");
  }

  async function replyThread(threadID: string) {
    const body = replyDrafts[threadID]?.trim();
    if (!body || !currentDoc) {
      return;
    }
    await api.replyThread(currentDoc.id, threadID, body);
    setReplyDrafts((current) => ({ ...current, [threadID]: "" }));
    await refreshThreads(currentDoc.id, "local");
    setStatus("Reply added");
  }

  async function setThreadStatus(threadID: string, action: "resolve" | "reopen") {
    if (!currentDoc) {
      return;
    }
    if (action === "resolve") {
      await api.resolveThread(currentDoc.id, threadID);
      setStatus("Comment resolved");
    } else {
      await api.reopenThread(currentDoc.id, threadID);
      setStatus("Comment reopened");
    }
    await refreshThreads(currentDoc.id, "local");
  }

  async function copyLink() {
    try {
      await navigator.clipboard.writeText(window.location.href);
      setStatus("Link copied");
    } catch {
      setStatus("Could not copy link.");
    }
  }

  function openThread(threadID: string) {
    if (!currentDoc) {
      return;
    }
    const thread = threads.find((item) => item.id === threadID);
    if (thread) {
      setCommentsView(thread.status);
    }
    clearThreadUnread(threadID, threads);
    setCommentsCollapsed(false);
    navigateTo({ documentId: currentDoc.id, threadId: threadID }, "replace");
  }

  function showCommentsView(nextView: CommentsView) {
    if (!currentDoc) {
      setCommentsView(nextView);
      return;
    }
    if (activeThread && activeThread.status !== nextView) {
      navigateTo({ documentId: currentDoc.id, threadId: null }, "replace");
    }
    setCommentsView(nextView);
  }

  function clearRoute() {
    navigateTo({ documentId: null, threadId: null });
  }

  function startCommentsResize(event: ReactPointerEvent<HTMLDivElement>) {
    if (window.innerWidth <= 1100) {
      return;
    }
    event.preventDefault();
    resizeStateRef.current = {
      startX: event.clientX,
      startWidth: commentsWidth,
    };
    document.body.classList.add("is-resizing-comments");
  }

  if (!route.documentId) {
    return (
      <div className="page-shell page-shell-home">
        <header className="page-header">
          <div>
            <p className="eyebrow">Document IDs</p>
            <h1>AgentPad</h1>
            <p className="page-subtitle">{status}</p>
          </div>
          <label className="inline-field">
            <span>Name</span>
            <input value={actor} onChange={(event) => setActor(event.target.value)} placeholder="Display name" />
          </label>
        </header>

        <main className="docs-page">
          <section className="panel">
            <div className="panel-header">
              <div>
                <p className="eyebrow">Create</p>
                <h2>New document</h2>
              </div>
            </div>

            <form className="form-grid" onSubmit={handleCreateSubmit}>
              <div className="open-surface-copy">
                <h3>Start a fresh doc and share it by URL</h3>
                <p>
                  AgentPad now routes documents by ID, so every doc opens at its own <code>/{`<id>`}</code> URL with optional thread deep links.
                </p>
              </div>

              <label className="stacked-field">
                <span>Title</span>
                <input
                  value={createTitle}
                  onChange={(event) => setCreateTitle(event.target.value)}
                  placeholder="Weekly notes"
                />
              </label>

              <label className="stacked-field">
                <span>Starting content</span>
                <textarea
                  rows={10}
                  value={createSource}
                  onChange={(event) => setCreateSource(event.target.value)}
                  placeholder="# Weekly notes&#10;&#10;Start writing here..."
                />
              </label>

              <div className="panel-actions">
                <button className="button" type="submit" disabled={isDocumentActionPending}>
                  Create document
                </button>
              </div>
            </form>
          </section>

          <section className="panel">
            <div className="panel-header">
              <div>
                <p className="eyebrow">Import</p>
                <h2>Bring in an existing file</h2>
              </div>
            </div>

            <form className="form-grid" onSubmit={handleImportSubmit}>
              <div className="open-surface-copy">
                <h3>Upload markdown or plain text</h3>
                <p>The backend imports the file into a document record, then AgentPad opens the new document ID route.</p>
              </div>

              <label className="stacked-field">
                <span>Choose file</span>
                <input
                  ref={importInputRef}
                  type="file"
                  accept=".md,.markdown,.txt,text/markdown,text/plain"
                  onChange={handleImportFileChange}
                />
              </label>

              <p className="page-subtitle">{importFile ? `Selected ${importFile.name}` : "No file selected yet."}</p>

              <div className="panel-actions">
                <button className="button secondary" type="submit" disabled={!importFile || isDocumentActionPending}>
                  Import file
                </button>
              </div>
            </form>
          </section>
        </main>
      </div>
    );
  }

  const docLayoutStyle = {
    "--comments-sidebar-width": `${commentsWidth}px`,
  } as CSSProperties;
  const commentsToggleLabel = commentsCollapsed ? `Show comments (${visibleThreads.length})` : "Hide comments";

  return (
    <div className="page-shell page-shell-doc">
      <header className="doc-header">
        <div className="doc-header-main">
          <button className="button secondary" onClick={clearRoute}>
            Home
          </button>
          <div className="doc-header-copy">
            <div className="doc-title-row">
              <p className="eyebrow">Document</p>
              <h1>{currentDoc?.title ?? "Loading document..."}</h1>
            </div>
            <div className="doc-meta-row">
              <span>{status}</span>
              {currentDoc ? (
                <>
                  <span aria-hidden="true">•</span>
                  <span className="doc-path" title={currentDoc.id}>
                    {currentDoc.id}
                  </span>
                </>
              ) : null}
            </div>
          </div>
        </div>

        <div className="doc-header-actions">
          <div className="doc-header-controls">
            <div className="presence-strip">
              {presence.length === 0 ? (
                <span className="status-tag muted">Just you</span>
              ) : (
                presence.map((person) => (
                  <span key={person.session_id} className="status-tag">
                    {person.name}
                  </span>
                ))
              )}
            </div>
            <label className="inline-field compact">
              <span>Name</span>
              <input value={actor} onChange={(event) => setActor(event.target.value)} placeholder="Display name" />
            </label>
            <button className="button secondary" onClick={() => setCommentsCollapsed((current) => !current)} aria-pressed={!commentsCollapsed}>
              {commentsToggleLabel}
            </button>
            <button className="button secondary" onClick={() => void copyLink()}>
              Copy link
            </button>
          </div>
        </div>
      </header>

      <main className={`doc-layout ${commentsCollapsed ? "doc-layout-comments-collapsed" : ""}`} style={docLayoutStyle}>
        <section className="doc-editor-column">
          {selection ? (
            <div className="selection-composer" style={composerStyle}>
              <p className="composer-label">Add comment</p>
              {selectionPreview ? <p className="composer-quote">"{selectionPreview}"</p> : null}
              <textarea
                value={commentBody}
                onChange={(event) => setCommentBody(event.target.value)}
                rows={3}
                placeholder="Write a comment on this selection"
              />
              <div className="panel-actions">
                <button className="button" onClick={() => void createThread()}>
                  Comment
                </button>
                <button
                  className="button secondary"
                  onClick={() => {
                    setSelection(null);
                    setCommentBody("");
                    editorRef.current?.clearSelection();
                  }}
                >
                  Cancel
                </button>
              </div>
            </div>
          ) : null}

          <EditorPane
            ref={editorRef}
            document={currentDoc}
            actor={actor}
            threads={highlightThreads}
            activeThreadId={activeThreadId}
            onThreadSelect={openThread}
            onSelectionChange={setSelection}
            onPresenceChange={setPresence}
            onRevisionChange={handleRevisionChange}
            onDocumentArtifactHint={handleDocumentArtifactHint}
            onThreadsArtifactHint={handleThreadsArtifactHint}
            onStatus={setStatus}
          />
        </section>

        {!commentsCollapsed ? (
          <>
            <div
              className="comments-resizer"
              role="separator"
              aria-label="Resize comments sidebar"
              aria-orientation="vertical"
              aria-valuemin={MIN_COMMENTS_WIDTH}
              aria-valuemax={getCommentsWidthMax(window.innerWidth)}
              aria-valuenow={commentsWidth}
              onPointerDown={startCommentsResize}
            />

            <aside className="comments-sidebar">
              <div className="comments-sidebar-header">
                <div>
                  <p className="eyebrow">Discussion</p>
                  <h2>Comments</h2>
                </div>
                <span className="status-tag">{visibleThreads.length}</span>
              </div>

              <div className="thread-view-tabs" role="tablist" aria-label="Comment visibility">
                <button
                  type="button"
                  role="tab"
                  aria-selected={commentsView === "open"}
                  className={`thread-view-tab ${commentsView === "open" ? "thread-view-tab-active" : ""}`}
                  onClick={() => showCommentsView("open")}
                >
                  Open
                  <span className="thread-view-tab-count">{openThreads.length}</span>
                </button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={commentsView === "resolved"}
                  className={`thread-view-tab ${commentsView === "resolved" ? "thread-view-tab-active" : ""}`}
                  onClick={() => showCommentsView("resolved")}
                >
                  Resolved
                  <span className="thread-view-tab-count">{resolvedThreads.length}</span>
                </button>
              </div>

              {visibleThreads.length === 0 ? (
                <div className="empty-state">
                  <h3>{commentsView === "open" ? "No open comments" : "No resolved comments"}</h3>
                  <p>
                    {commentsView === "open"
                      ? "Select text in the editor to start a thread."
                      : "Resolved threads will appear here."}
                  </p>
                </div>
              ) : (
                <div className="thread-list">
                  {visibleThreads.map((thread) => {
                    const isActive = thread.id === activeThreadId;
                    const unreadCommentCount = (thread.comments ?? []).filter((comment) => unreadCommentIds.has(comment.id)).length;
                    const isUnread = unreadThreadIds.has(thread.id) || unreadCommentCount > 0;
                    return (
                      <article
                        key={thread.id}
                        className={`thread-card ${isActive ? "thread-card-active" : ""} ${isUnread ? "thread-card-unread" : ""}`}
                        data-thread-card={thread.id}
                      >
                        <div className="thread-card-header">
                          <span className={`thread-state ${thread.status === "resolved" ? "thread-state-resolved" : ""}`}>{thread.status}</span>
                          <div className="thread-meta">
                            <span>{thread.comments.length} comment{thread.comments.length === 1 ? "" : "s"}</span>
                            {unreadCommentCount > 0 ? <span className="thread-unread-badge">{unreadCommentCount} new</span> : null}
                            <span>{formatTimestamp(thread.updated_at)}</span>
                          </div>
                        </div>

                        <blockquote className="thread-quote-block">
                          <button
                            className={`thread-quote-button ${isUnread ? "thread-quote-button-unread" : ""}`}
                            onClick={() => openThread(thread.id)}
                            title={threadQuoteTitle(thread)}
                          >
                            <span className="thread-quote">{formatQuote(thread)}</span>
                          </button>
                          {!thread.anchor.resolved ? (
                            <p className="thread-quote-note">Text changed after edits. Highlight hidden until it is re-anchored.</p>
                          ) : null}
                        </blockquote>

                        {isActive ? (
                          <div className="thread-detail">
                            <div className="comment-list">
                              {(thread.comments ?? []).map((comment) => (
                                <div
                                  key={comment.id}
                                  className={`comment-bubble ${unreadCommentIds.has(comment.id) ? "comment-bubble-unread" : ""}`}
                                >
                                  <div className="comment-bubble-meta">
                                    <strong>{comment.author}</strong>
                                    <span>{formatTimestamp(comment.created_at)}</span>
                                  </div>
                                  <p>{comment.body}</p>
                                </div>
                              ))}
                            </div>

                            <textarea
                              rows={3}
                              value={replyDrafts[thread.id] ?? ""}
                              onChange={(event) => setReplyDrafts((current) => ({ ...current, [thread.id]: event.target.value }))}
                              placeholder="Reply to this thread"
                            />

                            <div className="panel-actions thread-detail-actions">
                              <button className="button" onClick={() => void replyThread(thread.id)}>
                                Reply
                              </button>
                              {thread.status === "open" ? (
                                <button className="button secondary" onClick={() => void setThreadStatus(thread.id, "resolve")}>
                                  Resolve
                                </button>
                              ) : (
                                <button className="button secondary" onClick={() => void setThreadStatus(thread.id, "reopen")}>
                                  Reopen
                                </button>
                              )}
                            </div>
                          </div>
                        ) : null}
                      </article>
                    );
                  })}
                </div>
              )}

              {activeVisibleThread ? <div className="sidebar-footer">Active thread by {activeVisibleThread.author}</div> : null}
            </aside>
          </>
        ) : null}
      </main>
    </div>
  );
}
