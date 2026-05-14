import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const editorPaneMock = vi.hoisted(() => vi.fn());

vi.mock("./components/EditorPane", () => ({
  EditorPane: (props: unknown) => {
    editorPaneMock(props);
    return <div data-testid="editor-pane">editor</div>;
  },
}));

import App from "./App";
import type { Thread } from "./lib/types";

const documentId = "doc-123";
const mockDocument = {
  id: documentId,
  title: "Spec",
  format: "markdown" as const,
  source: "# Title\n\nHello world",
  revision: 0,
  blocks: [],
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
};

function jsonResponse(payload: unknown) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function makeThread(overrides: Partial<Thread> = {}): Thread {
  return {
    id: overrides.id ?? "thread-1",
    document_id: overrides.document_id ?? documentId,
    anchor: overrides.anchor ?? {
      block_id: "block-1",
      start: 0,
      end: 5,
      doc_start: 0,
      doc_end: 5,
      quote: "Hello",
      revision: 0,
      resolved: true,
    },
    status: overrides.status ?? "open",
    author: overrides.author ?? "tester",
    comments: overrides.comments ?? [
      {
        id: `${overrides.id ?? "thread-1"}-comment-1`,
        thread_id: overrides.id ?? "thread-1",
        author: "tester",
        body: "Comment body",
        created_at: new Date().toISOString(),
      },
    ],
    created_at: overrides.created_at ?? new Date().toISOString(),
    updated_at: overrides.updated_at ?? new Date().toISOString(),
  };
}

describe("App", () => {
  beforeEach(() => {
    editorPaneMock.mockClear();
    HTMLElement.prototype.scrollIntoView = vi.fn();
    window.history.replaceState({}, "", "/");
    localStorage.clear();
    global.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input.toString();
      const method = init?.method ?? "GET";
      if (url.endsWith("/api/documents") && method === "POST") {
        return jsonResponse(mockDocument);
      }
      if (url.endsWith("/api/documents/import") && method === "POST") {
        return jsonResponse(mockDocument);
      }
      if (url.endsWith(`/api/documents/${documentId}`)) {
        return jsonResponse(mockDocument);
      }
      if (url.endsWith(`/api/documents/${documentId}/threads`)) {
        return jsonResponse([]);
      }
      if (url.endsWith(`/api/documents/${documentId}/activity`)) {
        return jsonResponse([]);
      }
      return new Response(JSON.stringify({ message: "Not found" }), {
        status: 404,
        headers: { "Content-Type": "application/json" },
      });
    }) as typeof fetch;
  });

  it("creates a document from the landing page and navigates to its id route", async () => {
    render(<App />);

    fireEvent.change(screen.getByLabelText(/^title$/i), {
      target: { value: "Spec" },
    });
    fireEvent.change(screen.getByLabelText(/starting content/i), {
      target: { value: "# Title\n\nHello world" },
    });
    fireEvent.click(screen.getByRole("button", { name: /create document/i }));

    await waitFor(() => expect(screen.getByRole("heading", { name: "Comments" })).toBeTruthy());
    expect(window.location.pathname).toBe(`/${documentId}`);
    expect(screen.getByTestId("editor-pane")).toBeTruthy();
  });

  it("imports a file from the landing page", async () => {
    render(<App />);

    const file = new File(["# Imported"], "notes.md", { type: "text/markdown" });
    fireEvent.change(screen.getByLabelText(/choose file/i), {
      target: { files: [file] },
    });
    fireEvent.click(screen.getByRole("button", { name: /import file/i }));

    await waitFor(() => expect(screen.getByRole("heading", { name: "Comments" })).toBeTruthy());
    expect(window.location.pathname).toBe(`/${documentId}`);

    const importCall = (global.fetch as ReturnType<typeof vi.fn>).mock.calls.find(
      ([input, init]) => input.toString().endsWith("/api/documents/import") && (init?.method ?? "GET") === "POST",
    );
    expect(importCall).toBeTruthy();
    expect(importCall?.[1]?.body).toBeInstanceOf(FormData);
  });

  it("loads a document from the pathname and reads the thread query", async () => {
    window.history.replaceState({}, "", `/${documentId}?thread=thread-1`);

    render(<App />);

    await waitFor(() => expect(screen.getByRole("heading", { name: "Comments" })).toBeTruthy());
    await waitFor(() => {
      const props = editorPaneMock.mock.calls.at(-1)?.[0] as { activeThreadId: string | null } | undefined;
      expect(props?.activeThreadId).toBe("thread-1");
    });
  });

  it("shows open threads by default, switches views, and writes thread deep links", async () => {
    const threads = [
      makeThread({
        id: "thread-open",
        status: "open",
        anchor: {
          block_id: "block-open",
          start: 0,
          end: 5,
          doc_start: 0,
          doc_end: 5,
          quote: "Open quote",
          revision: 0,
          resolved: true,
        },
      }),
      makeThread({
        id: "thread-resolved",
        status: "resolved",
        anchor: {
          block_id: "block-resolved",
          start: 6,
          end: 14,
          doc_start: 6,
          doc_end: 14,
          quote: "Resolved quote",
          revision: 0,
          resolved: true,
        },
        comments: [
          {
            id: "thread-resolved-comment-1",
            thread_id: "thread-resolved",
            author: "tester",
            body: "Resolved body",
            created_at: new Date().toISOString(),
          },
        ],
      }),
    ];

    global.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith(`/api/documents/${documentId}`)) {
        return jsonResponse(mockDocument);
      }
      if (url.endsWith(`/api/documents/${documentId}/threads`)) {
        return jsonResponse(threads);
      }
      return new Response(JSON.stringify({ message: "Not found" }), {
        status: 404,
        headers: { "Content-Type": "application/json" },
      });
    }) as typeof fetch;

    window.history.replaceState({}, "", `/${documentId}`);
    render(<App />);

    await waitFor(() => expect(screen.getByRole("heading", { name: "Comments" })).toBeTruthy());
    await waitFor(() => expect(screen.getByText("Open quote")).toBeTruthy());
    expect(screen.queryByText("Resolved quote")).toBeNull();
    await waitFor(() => {
      const props = editorPaneMock.mock.calls.at(-1)?.[0] as { threads: Thread[] } | undefined;
      expect(props?.threads.map((thread) => thread.id)).toEqual(["thread-open"]);
    });

    fireEvent.click(screen.getByRole("tab", { name: /resolved/i }));

    await waitFor(() => expect(screen.getByText("Resolved quote")).toBeTruthy());
    expect(screen.queryByText("Open quote")).toBeNull();
    await waitFor(() => {
      const props = editorPaneMock.mock.calls.at(-1)?.[0] as { threads: Thread[] } | undefined;
      expect(props?.threads.map((thread) => thread.id)).toEqual(["thread-open"]);
    });

    fireEvent.click(screen.getByRole("button", { name: /resolved quote/i }));

    await waitFor(() => {
      const props = editorPaneMock.mock.calls.at(-1)?.[0] as { threads: Thread[] } | undefined;
      expect(props?.threads.map((thread) => thread.id)).toEqual(["thread-open"]);
    });
    expect(window.location.pathname).toBe(`/${documentId}`);
    expect(new URLSearchParams(window.location.search).get("thread")).toBe("thread-resolved");

    const resolvedHighlightsToggle = screen.getByLabelText(/resolved highlights/i) as HTMLInputElement;
    expect(resolvedHighlightsToggle.checked).toBe(false);
    fireEvent.click(resolvedHighlightsToggle);

    await waitFor(() => {
      const props = editorPaneMock.mock.calls.at(-1)?.[0] as { threads: Thread[]; showResolvedThreadHighlights: boolean } | undefined;
      expect(props?.showResolvedThreadHighlights).toBe(true);
      expect(props?.threads.map((thread) => thread.id)).toEqual(["thread-open", "thread-resolved"]);
    });
  });
});
