# AgentPad Agent Usage


## When To Use AgentPad

Use AgentPad when the user wants collaboration on a local document:

- review feedback
- threaded discussion
- comment replies or resolution
- opening a doc in the AgentPad UI
- document edits that should preserve thread context

## Core Rules

- The server is the only writer. Do not edit AgentPad metadata files directly.
- For AgentPad-managed documents, do not patch the source file directly. Use the AgentPad CLI for document edits.
- Treat the absolute file path as the document identifier.
- You can read the file on disk normally for broad understanding.
- Before changing document content or thread state, use AgentPad to inspect the current document state and get the right deep link.
- Use `agentpad read` when you need current AgentPad-scoped text or an anchor for an edit or thread action.
- Prefer the smallest localized edit that solves the task.
- If you are addressing a specific thread, prefer `agentpad edit --thread <thread-id>` so AgentPad can retarget the highlight onto the replacement span.
- For several disjoint localized edits, prefer `agentpad edit-many`.
- Use `--body-file` or `--text-file` for multiline content. Do not rely on shell-escaped `\n`.

## Default Workflow

1. Check server readiness:
   `curl -fsS http://127.0.0.1:8080/api/health`
2. If needed, start the server:
   `agentpad serve`
3. If you need broad context, read the file on disk normally.
4. Before changing the document or thread state, inspect the AgentPad document:
   `agentpad inspect /absolute/path/to/file.md --json`
5. List lightweight thread summaries before loading full bodies:
   `agentpad threads list /absolute/path/to/file.md --summary --json`
6. Fetch a specific thread only when you need its full comments:
   `agentpad threads get /absolute/path/to/file.md <thread-id> --json`
7. Before an edit or a new thread, read only the narrow scope you need. Prefer an anchor-first read:
   `agentpad read /absolute/path/to/file.md --quote "old text" --prefix "before " --suffix " after" --anchor-only --json`
8. Apply the edit through AgentPad:
   `agentpad edit /absolute/path/to/file.md --anchor-file /tmp/anchor.json --text "replacement text" --json`
9. If you are addressing an existing thread, use a thread-aware edit instead:
   `agentpad edit /absolute/path/to/file.md --thread <thread-id> --text "replacement text" --json`
10. If a thread becomes unresolved after an edit, inspect it and explicitly re-anchor it:
   `agentpad threads get /absolute/path/to/file.md <thread-id> --json`
   `agentpad read /absolute/path/to/file.md --quote "replacement text" --prefix "before " --suffix " after" --anchor-only --json > /tmp/anchor.json`
   `agentpad threads reanchor /absolute/path/to/file.md <thread-id> --anchor-file /tmp/anchor.json --json`
11. Reply to or resolve threads after the document change, then share the deep link:
   `agentpad open /absolute/path/to/file.md --json`

## Notes

- For whole-document understanding, a normal disk read is often fine.
- Prefer `agentpad inspect` and targeted `agentpad read` when you need current AgentPad state, anchors, or deep links.
- `agentpad open --json` returns a lightweight summary by default. Add `--include-document` only when you truly need the full payload from AgentPad itself.
- `agentpad read` omits block metadata by default. Add `--full` only when block metadata is worth the extra payload.
- Prefer `threads list --summary` before `threads get`.
- Use `threads reanchor` only after choosing a fresh current span; it is the fallback path when a thread can no longer resolve automatically.
- When the user wants the browser UI, give them the deep link returned by `inspect` or `open --json`.
- When passing text that includes command examples through the shell, avoid embedding backticked snippets inline in a quoted argument. Shells can treat backticks as command substitution and `<thread-id>`-style placeholders as redirection.
- Prefer `--body-file`, `--text-file`, JSON edit files, or a quoted heredoc when the text you are sending includes command examples, backticks, angle brackets, or multiline content.
