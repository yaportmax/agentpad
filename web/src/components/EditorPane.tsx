import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from "react";
import { basicSetup } from "codemirror";
import { markdown } from "@codemirror/lang-markdown";
import { Annotation, EditorSelection, EditorState, RangeSetBuilder, StateEffect, StateField } from "@codemirror/state";
import { Decoration, type DecorationSet, EditorView, keymap, WidgetType } from "@codemirror/view";

import { wsURL } from "../lib/api";
import type { Document, LiveMessage, Operation, Presence, SelectionRange, Thread } from "../lib/types";
import { applyOperation, sliceByCodePoint, toCodePointOffset, toCodeUnitOffset, toEditorChange, transformAgainst } from "../lib/ot";
import {
  buildRemoteArtifactsForOperation,
  getRemoteArtifactTitle,
  mapRemoteArtifacts,
  type RemoteArtifact,
  type RemoteDeleteArtifact,
} from "../lib/remoteArtifacts";

const remoteAnnotation = Annotation.define<boolean>();
const setThreadDecorations = StateEffect.define<DecorationSet>();
const addRemoteArtifacts = StateEffect.define<RemoteArtifact[]>();
const clearRemoteArtifactGroup = StateEffect.define<string>();

interface RemoteDecorationState {
  artifacts: RemoteArtifact[];
  decorations: DecorationSet;
}

class DeletedTextWidget extends WidgetType {
  constructor(private artifact: RemoteDeleteArtifact) {
    super();
  }

  eq(other: DeletedTextWidget) {
    return (
      other.artifact.id === this.artifact.id &&
      other.artifact.text === this.artifact.text &&
      other.artifact.author === this.artifact.author
    );
  }

  toDOM() {
    const wrapper = document.createElement("span");
    wrapper.className = "cm-remote-change cm-remote-change-delete";
    wrapper.dataset.remoteArtifactId = this.artifact.id;
    wrapper.dataset.remoteArtifactGroupId = this.artifact.groupId;
    wrapper.dataset.remoteArtifactKind = this.artifact.kind;
    wrapper.title = formatDeletedArtifactTitle(this.artifact);

    const deletedText = document.createElement("span");
    deletedText.className = "cm-remote-change-delete-text";
    deletedText.textContent = formatDeletedTextPreview(this.artifact.text);

    const authorChip = document.createElement("span");
    authorChip.className = "cm-remote-change-author";
    authorChip.textContent = this.artifact.author;

    wrapper.append(deletedText, authorChip);
    return wrapper;
  }

  ignoreEvent() {
    return false;
  }
}

function formatDeletedTextPreview(text: string) {
  const normalized = text.replace(/\s+/g, " ").trim();
  if (!normalized) {
    return "deleted text";
  }
  return normalized.length > 72 ? `${normalized.slice(0, 69)}...` : normalized;
}

function formatDeletedArtifactTitle(artifact: RemoteDeleteArtifact) {
  const preview = artifact.text.trim();
  if (!preview) {
    return getRemoteArtifactTitle(artifact);
  }
  return `${getRemoteArtifactTitle(artifact)}\n${preview}`;
}

const threadDecorationField = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(value, transaction) {
    value = value.map(transaction.changes);
    for (const effect of transaction.effects) {
      if (effect.is(setThreadDecorations)) {
        return effect.value;
      }
    }
    return value;
  },
  provide: (field) => EditorView.decorations.from(field),
});

const remoteDecorationField = StateField.define<RemoteDecorationState>({
  create: () => ({
    artifacts: [],
    decorations: Decoration.none,
  }),
  update(value, transaction) {
    let artifacts = transaction.docChanged ? mapRemoteArtifacts(value.artifacts, transaction.changes) : value.artifacts;
    let shouldRebuild = transaction.docChanged;

    for (const effect of transaction.effects) {
      if (effect.is(addRemoteArtifacts)) {
        artifacts = [...artifacts, ...effect.value];
        shouldRebuild = true;
      }
      if (effect.is(clearRemoteArtifactGroup)) {
        const nextArtifacts = artifacts.filter((artifact) => artifact.groupId !== effect.value);
        if (nextArtifacts.length !== artifacts.length) {
          artifacts = nextArtifacts;
          shouldRebuild = true;
        }
      }
    }

    if (!shouldRebuild) {
      return value;
    }

    return {
      artifacts,
      decorations: buildRemoteDecorations(artifacts),
    };
  },
  provide: (field) => EditorView.decorations.from(field, (value) => value.decorations),
});

export interface EditorPaneHandle {
  focusRange(start: number, end: number): void;
  clearSelection(): void;
}

interface EditorPaneProps {
  document: Document | null;
  actor: string;
  threads: Thread[];
  activeThreadId: string | null;
  showResolvedThreadHighlights: boolean;
  onThreadSelect: (threadID: string) => void;
  onSelectionChange: (selection: SelectionRange | null) => void;
  onPresenceChange: (presence: Presence[]) => void;
  onRevisionChange: (revision: number, source: string) => void;
  onDocumentArtifactHint: () => void;
  onThreadsArtifactHint: () => void;
  onStatus: (status: string) => void;
}

function clamp(value: number, min: number, max: number) {
  return Math.min(Math.max(value, min), max);
}

function buildSelectionRect(view: EditorView, from: number, to: number) {
  const startCoords = view.coordsAtPos(from);
  const endCoords = view.coordsAtPos(to);
  const anchor = startCoords ?? endCoords;
  if (!anchor) {
    return undefined;
  }
  return {
    left: anchor.left,
    top: (startCoords ?? endCoords)?.top ?? anchor.top,
    bottom: Math.max(startCoords?.bottom ?? anchor.bottom, endCoords?.bottom ?? anchor.bottom),
  };
}

function shouldHighlightThread(thread: Thread, showResolvedThreadHighlights: boolean) {
  return thread.anchor.resolved && (thread.status === "open" || showResolvedThreadHighlights);
}

function buildThreadDecorations(
  source: string,
  threads: Thread[],
  activeThreadId: string | null,
  showResolvedThreadHighlights: boolean,
) {
  const builder = new RangeSetBuilder<Decoration>();
  const sortedThreads = threads
    .filter((thread) => shouldHighlightThread(thread, showResolvedThreadHighlights))
    .sort((left, right) => {
      if (left.anchor.doc_start !== right.anchor.doc_start) {
        return left.anchor.doc_start - right.anchor.doc_start;
      }
      return left.anchor.doc_end - right.anchor.doc_end;
    });

  for (const thread of sortedThreads) {
    const from = clamp(toCodeUnitOffset(source, thread.anchor.doc_start), 0, source.length);
    const to = clamp(toCodeUnitOffset(source, thread.anchor.doc_end), from, source.length);
    if (from === to) {
      continue;
    }
    builder.add(
      from,
      to,
      Decoration.mark({
        class: `cm-thread-highlight ${thread.id === activeThreadId ? "cm-thread-highlight-active" : ""}`,
      }),
    );
  }

  return builder.finish();
}

function buildRemoteDecorations(artifacts: RemoteArtifact[]) {
  const builder = new RangeSetBuilder<Decoration>();
  const sortedArtifacts = [...artifacts].sort((left, right) => {
    const leftPosition = left.kind === "delete" ? left.position : left.from;
    const rightPosition = right.kind === "delete" ? right.position : right.from;
    if (leftPosition !== rightPosition) {
      return leftPosition - rightPosition;
    }
    if (left.kind === "delete" && right.kind !== "delete") {
      return -1;
    }
    if (left.kind !== "delete" && right.kind === "delete") {
      return 1;
    }
    return left.id.localeCompare(right.id);
  });

  for (const artifact of sortedArtifacts) {
    if (artifact.kind === "delete") {
      builder.add(
        artifact.position,
        artifact.position,
        Decoration.widget({
          widget: new DeletedTextWidget(artifact),
          side: 1,
        }),
      );
      continue;
    }

    builder.add(
      artifact.from,
      artifact.to,
      Decoration.mark({
        class: `cm-remote-change cm-remote-change-${artifact.kind}`,
        attributes: {
          "data-remote-artifact-id": artifact.id,
          "data-remote-artifact-group-id": artifact.groupId,
          "data-remote-artifact-kind": artifact.kind,
          title: getRemoteArtifactTitle(artifact),
        },
      }),
    );
  }

  return builder.finish();
}

export const EditorPane = forwardRef<EditorPaneHandle, EditorPaneProps>(function EditorPane(
  {
    document,
    actor,
    threads,
    activeThreadId,
    showResolvedThreadHighlights,
    onThreadSelect,
    onSelectionChange,
    onPresenceChange,
    onRevisionChange,
    onDocumentArtifactHint,
    onThreadsArtifactHint,
    onStatus,
  },
  ref,
) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const viewRef = useRef<EditorView | null>(null);
  const socketRef = useRef<WebSocket | null>(null);
  const serverRevisionRef = useRef(0);
  const pendingRef = useRef<Operation | null>(null);
  const bufferedRef = useRef<Operation[]>([]);
  const sessionIDRef = useRef<string | null>(null);
  const threadsRef = useRef<Thread[]>(threads);
  const showResolvedThreadHighlightsRef = useRef(showResolvedThreadHighlights);
  const onThreadSelectRef = useRef(onThreadSelect);
  const [connected, setConnected] = useState(false);

  useEffect(() => {
    threadsRef.current = threads;
    showResolvedThreadHighlightsRef.current = showResolvedThreadHighlights;
    onThreadSelectRef.current = onThreadSelect;
  }, [threads, showResolvedThreadHighlights, onThreadSelect]);

  useImperativeHandle(ref, () => ({
    focusRange(start: number, _end: number) {
      if (!viewRef.current) {
        return;
      }
      const source = viewRef.current.state.doc.toString();
      const cursor = toCodeUnitOffset(source, start);
      viewRef.current.dispatch({
        selection: EditorSelection.cursor(cursor),
        scrollIntoView: true,
      });
      viewRef.current.focus();
    },
    clearSelection() {
      if (!viewRef.current) {
        return;
      }
      const head = viewRef.current.state.selection.main.head;
      viewRef.current.dispatch({
        selection: EditorSelection.cursor(head),
      });
    },
  }));

  useEffect(() => {
    if (!containerRef.current || !document) {
      return;
    }

    const sendSelection = (view: EditorView) => {
      const selection = view.state.selection.main;
      const source = view.state.doc.toString();
      const start = toCodePointOffset(source, selection.from);
      const end = toCodePointOffset(source, selection.to);
      const selectedText = sliceByCodePoint(source, start, end);
      const rect = selection.empty ? undefined : buildSelectionRect(view, selection.from, selection.to);

      onSelectionChange(
        selection.empty
          ? null
          : {
              start,
              end,
              text: selectedText,
              rect,
            },
      );

      if (socketRef.current?.readyState === WebSocket.OPEN) {
        socketRef.current.send(
          JSON.stringify({
            type: "presence.update",
            selection: selection.empty ? null : { start, end },
          }),
        );
      }
    };

    const state = EditorState.create({
      doc: document.source,
      extensions: [
        basicSetup,
        markdown(),
        keymap.of([]),
        EditorView.lineWrapping,
        threadDecorationField,
        remoteDecorationField,
        EditorView.theme({
          "&": {
            height: "100%",
            fontSize: "15px",
          },
          ".cm-scroller": {
            overflow: "auto",
          },
        }),
        EditorView.domEventHandlers({
          click(event, view) {
            const remoteArtifactGroupID = (event.target as HTMLElement | null)?.closest<HTMLElement>("[data-remote-artifact-group-id]")
              ?.dataset.remoteArtifactGroupId;
            if (remoteArtifactGroupID) {
              view.dispatch({
                effects: clearRemoteArtifactGroup.of(remoteArtifactGroupID),
              });
              return true;
            }
            if (!view.state.selection.main.empty) {
              return false;
            }
            const position = view.posAtCoords({ x: event.clientX, y: event.clientY });
            if (position === null) {
              return false;
            }
            const source = view.state.doc.toString();
            const codePointPosition = toCodePointOffset(source, position);
            const thread = threadsRef.current.find(
              (item) =>
                shouldHighlightThread(item, showResolvedThreadHighlightsRef.current) &&
                codePointPosition >= item.anchor.doc_start &&
                codePointPosition <= item.anchor.doc_end,
            );
            if (!thread) {
              return false;
            }
            onThreadSelectRef.current(thread.id);
            return false;
          },
        }),
        EditorView.updateListener.of((update) => {
          const isRemote = update.transactions.some((transaction) => transaction.annotation(remoteAnnotation));
          if (update.selectionSet) {
            sendSelection(update.view);
          }
          if (!update.docChanged || isRemote || !socketRef.current || socketRef.current.readyState !== WebSocket.OPEN) {
            return;
          }
          const nextOps: Operation[] = [];
          const previousSource = update.startState.doc.toString();
          update.changes.iterChanges((fromA, toA, _fromB, _toB, inserted) => {
            const start = toCodePointOffset(previousSource, fromA);
            const end = toCodePointOffset(previousSource, toA);
            nextOps.push({
              position: start,
              delete_count: end - start,
              insert_text: inserted.toString(),
              base_revision: serverRevisionRef.current,
            });
          });
          for (const op of nextOps) {
            if (!pendingRef.current) {
              pendingRef.current = { ...op, base_revision: serverRevisionRef.current };
              socketRef.current.send(JSON.stringify({ type: "op.submit", op: pendingRef.current }));
            } else {
              bufferedRef.current = [...bufferedRef.current, op];
            }
          }
          onRevisionChange(serverRevisionRef.current, update.state.doc.toString());
        }),
      ],
    });

    const view = new EditorView({
      state,
      parent: containerRef.current,
    });

    viewRef.current = view;
    view.dispatch({
      effects: setThreadDecorations.of(
        buildThreadDecorations(document.source, threadsRef.current, activeThreadId, showResolvedThreadHighlightsRef.current),
      ),
    });
    onSelectionChange(null);

    return () => {
      view.destroy();
      viewRef.current = null;
    };
  }, [document?.id]);

  useEffect(() => {
    if (!document || !viewRef.current) {
      return;
    }
    const current = viewRef.current.state.doc.toString();
    if (current !== document.source) {
      viewRef.current.dispatch({
        changes: { from: 0, to: current.length, insert: document.source },
        annotations: remoteAnnotation.of(true),
      });
    }
    serverRevisionRef.current = document.revision;
  }, [document?.source, document?.revision, document?.id]);

  useEffect(() => {
    if (!viewRef.current) {
      return;
    }
    const source = viewRef.current.state.doc.toString();
    viewRef.current.dispatch({
      effects: setThreadDecorations.of(buildThreadDecorations(source, threads, activeThreadId, showResolvedThreadHighlights)),
    });
  }, [document?.id, document?.source, threads, activeThreadId, showResolvedThreadHighlights]);

  useEffect(() => {
    if (!document) {
      return;
    }
    let active = true;
    const socket = new WebSocket(wsURL(document.id, actor));
    socketRef.current = socket;
    pendingRef.current = null;
    bufferedRef.current = [];
    setConnected(false);
    onPresenceChange([]);
    onStatus("Connecting to live session...");

    socket.addEventListener("open", () => {
      if (!active || socketRef.current !== socket) {
        return;
      }
      setConnected(true);
      onStatus("Connected");
    });
    socket.addEventListener("error", () => {
      if (!active || socketRef.current !== socket) {
        return;
      }
      setConnected(false);
      onStatus("Disconnected");
    });
    socket.addEventListener("close", () => {
      if (!active || socketRef.current !== socket) {
        return;
      }
      setConnected(false);
      socketRef.current = null;
      onStatus("Disconnected");
    });
    socket.addEventListener("message", (event) => {
      if (!active || socketRef.current !== socket) {
        return;
      }
      const message = JSON.parse(event.data) as LiveMessage;
      if (message.error) {
        onStatus(message.error.message);
        return;
      }
      if (message.type === "snapshot") {
        sessionIDRef.current = message.session_id ?? null;
        if (message.document && viewRef.current) {
          const current = viewRef.current.state.doc.toString();
          if (current !== message.document.source) {
            viewRef.current.dispatch({
              changes: { from: 0, to: current.length, insert: message.document.source },
              annotations: remoteAnnotation.of(true),
            });
          }
          serverRevisionRef.current = message.document.revision;
          onRevisionChange(message.document.revision, message.document.source);
        }
        onPresenceChange(message.presence ?? []);
        return;
      }
      if (message.type === "presence.changed") {
        onPresenceChange(message.presence ?? []);
        return;
      }
      if (message.type === "artifact.changed") {
        if (message.artifact === "threads") {
          onThreadsArtifactHint();
          return;
        }
        onDocumentArtifactHint();
        return;
      }
      if (message.type === "op.ack") {
        pendingRef.current = null;
        if (message.document && viewRef.current) {
          const local = viewRef.current.state.doc.toString();
          if (local !== message.document.source) {
            viewRef.current.dispatch({
              changes: { from: 0, to: local.length, insert: message.document.source },
              annotations: remoteAnnotation.of(true),
            });
          }
          serverRevisionRef.current = message.document.revision;
          onRevisionChange(message.document.revision, message.document.source);
        } else if (typeof message.revision === "number" && viewRef.current) {
          serverRevisionRef.current = message.revision;
          onRevisionChange(message.revision, viewRef.current.state.doc.toString());
        }
        const next = bufferedRef.current[0];
        if (next) {
          bufferedRef.current = bufferedRef.current.slice(1);
          pendingRef.current = { ...next, base_revision: serverRevisionRef.current };
          socket.send(JSON.stringify({ type: "op.submit", op: pendingRef.current }));
        }
        return;
      }
      if (message.type === "op.applied" && message.op && viewRef.current) {
        const canonical = message.op;
        let incoming = canonical;
        if (pendingRef.current) {
          incoming = transformAgainst(incoming, pendingRef.current);
          pendingRef.current = transformAgainst(pendingRef.current, canonical);
        }
        if (bufferedRef.current.length > 0) {
          bufferedRef.current = bufferedRef.current.map((localOp) => {
            incoming = transformAgainst(incoming, localOp);
            return transformAgainst(localOp, canonical);
          });
        }
        const previousSource = viewRef.current.state.doc.toString();
        const nextSource = applyOperation(previousSource, incoming);
        const nextRemoteArtifacts =
          canonical.author && canonical.author !== actor
            ? buildRemoteArtifactsForOperation(previousSource, nextSource, {
                ...incoming,
                author: canonical.author,
              })
            : [];
        const remoteChange = toEditorChange(previousSource, incoming);
        viewRef.current.dispatch({
          changes: remoteChange,
          annotations: remoteAnnotation.of(true),
          effects: nextRemoteArtifacts.length > 0 ? addRemoteArtifacts.of(nextRemoteArtifacts) : [],
        });
        serverRevisionRef.current = message.revision ?? serverRevisionRef.current + 1;
        onRevisionChange(serverRevisionRef.current, nextSource);
      }
    });

    return () => {
      active = false;
      if (socketRef.current === socket) {
        socketRef.current = null;
      }
      pendingRef.current = null;
      bufferedRef.current = [];
      if (socket.readyState === WebSocket.CONNECTING || socket.readyState === WebSocket.OPEN) {
        socket.close();
      }
    };
  }, [document?.id, actor]);

  return (
    <div className="editor-shell">
      <div className="editor-toolbar">
        <span className={`status-tag ${connected ? "status-tag-live" : "status-tag-muted"}`}>
          {connected ? "Live" : "Offline"}
        </span>
        <span className="editor-meta">{document ? `${document.format} • rev ${document.revision}` : "No document selected"}</span>
      </div>
      <div className="editor-pane" ref={containerRef} />
    </div>
  );
});
