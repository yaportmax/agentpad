import type {
  ActivityEvent,
  Document,
  Thread,
} from "./types";

const API_BASE = (import.meta.env.VITE_AGENTPAD_API as string | undefined)?.replace(/\/$/, "") ?? "";

function buildURL(path: string, query?: Record<string, string>) {
  const base = API_BASE || window.location.origin;
  const url = new URL(path, base);
  for (const [key, value] of Object.entries(query ?? {})) {
    url.searchParams.set(key, value);
  }
  return API_BASE ? url.toString() : `${url.pathname}${url.search}`;
}

async function request<T>(path: string, init?: RequestInit, actor?: string, query?: Record<string, string>): Promise<T> {
  const headers = new Headers(init?.headers ?? {});
  if (!(init?.body instanceof FormData) && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  headers.set("X-AgentPad-Actor", actor ?? localStorage.getItem("agentpad.actor") ?? "browser-user");
  const response = await fetch(buildURL(path, query), {
    ...init,
    headers,
  });
  if (!response.ok) {
    const err = await response.json().catch(() => ({ message: response.statusText }));
    throw new Error(err.message ?? err.code ?? `Request failed with status ${response.status}`);
  }
  if (response.headers.get("content-type")?.includes("application/json")) {
    return response.json() as Promise<T>;
  }
  return (await response.text()) as T;
}

export const api = {
  listDocuments: () => request<Document[]>("/api/documents"),
  getDocument: (documentID: string) => request<Document>(`/api/documents/${encodeURIComponent(documentID)}`),
  createDocument: (payload: { title?: string; source?: string; format?: Document["format"] } = {}) =>
    request<Document>("/api/documents", { method: "POST", body: JSON.stringify(payload) }),
  importDocument: (file: File) => {
    const formData = new FormData();
    formData.append("file", file, file.name);
    return request<Document>("/api/documents/import", { method: "POST", body: formData });
  },
  exportDocument: async (documentID: string, format: string) => {
    const response = await fetch(buildURL(`/api/documents/${encodeURIComponent(documentID)}/export`, { format }), {
      headers: {
        "X-AgentPad-Actor": localStorage.getItem("agentpad.actor") ?? "browser-user",
      },
    });
    if (!response.ok) {
      throw new Error("Export failed");
    }
    return response.blob();
  },
  listThreads: (documentID: string) => request<Thread[]>(`/api/documents/${encodeURIComponent(documentID)}/threads`),
  createThread: (documentID: string, payload: { body: string; start: number; end: number }) =>
    request<Thread>(`/api/documents/${encodeURIComponent(documentID)}/threads`, {
      method: "POST",
      body: JSON.stringify(payload),
    }),
  replyThread: (documentID: string, threadID: string, body: string) =>
    request<{ thread: Thread }>(`/api/documents/${encodeURIComponent(documentID)}/threads/${encodeURIComponent(threadID)}/replies`, {
      method: "POST",
      body: JSON.stringify({ body }),
    }),
  resolveThread: (documentID: string, threadID: string) =>
    request<Thread>(`/api/documents/${encodeURIComponent(documentID)}/threads/${encodeURIComponent(threadID)}/resolve`, {
      method: "POST",
      body: JSON.stringify({}),
    }),
  reopenThread: (documentID: string, threadID: string) =>
    request<Thread>(`/api/documents/${encodeURIComponent(documentID)}/threads/${encodeURIComponent(threadID)}/reopen`, {
      method: "POST",
      body: JSON.stringify({}),
    }),
  listActivity: (documentID: string) => request<ActivityEvent[]>(`/api/documents/${encodeURIComponent(documentID)}/activity`),
};

export function wsURL(documentID: string, actor: string) {
  const base = API_BASE || window.location.origin;
  const url = new URL(`${base}/api/documents/${encodeURIComponent(documentID)}/live`);
  url.searchParams.set("name", actor);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  return url.toString();
}
