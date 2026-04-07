# Codex Interface Plan for AgentPad

## Why this plan exists

Session `019d544a-ca2d-7432-8ed3-a53403d4d384` shows that the current AgentPad interface exposed to Codex is workable but not agent-native. The biggest issue is not just that single responses are verbose. It is that the current skill, CLI defaults, and JSON shapes steer the model into a high-token, multi-step workflow before it can do useful work.

A second issue is now clear from real usage: comment highlights can drift after edits and end up marking unrelated text. That makes collaboration feel untrustworthy, and it means the Codex-facing interface should not just optimize for low tokens. It also needs to preserve comment-to-text truthfulness across edits.

## What the sessions showed

### Evidence from the original Codex session

- The session finished with a last-step input size of about `102k` tokens, and the overall turn consumed about `1.06M` input tokens.
- AgentPad-related command outputs contributed about `16k` output tokens on their own.
- The first three AgentPad calls were already expensive:
  - `agentpad open ... --json`: about `4279` output tokens
  - `agentpad read ... --json`: about `4109` output tokens
  - `agentpad threads list ... --json`: about `1173` output tokens
- That means roughly `9561` output tokens were spent before the review had even been grounded in code.
- The session made `9` `agentpad read` calls totaling about `8118` output tokens.
- The write path required shell composition and temp files instead of a direct agent-friendly handoff.

### Evidence from the highlight-drift investigation

- A thread created on one span can still come back from `threads list` with the original `doc_start` and `doc_end` even after that exact quoted text has been replaced.
- In a minimal repro, a thread created on `beta gamma` still returned `doc_start=6, doc_end=16` after the document was edited to `Alpha TEAM.`, which means the highlight now points at unrelated text.
- The backend currently tries to re-resolve thread anchors on read, but if resolution fails it silently returns the old anchor instead of surfacing an unresolved state.
- The frontend refreshes thread data on thread events, but not after ordinary document edits, so stale highlight positions can survive even when the current document text has already changed.

### Interface issues surfaced by the sessions

1. `open --json` is too heavy for its likely purpose.
   The command name suggests navigation, but the JSON payload returns the full document structure, including blocks. In practice the agent usually wants one of two things: a browser deep link or a tiny metadata object.

2. `read --json` duplicates content and defaults to the wrong shape.
   A full read returns both `text` and `blocks`, which mostly duplicate the same document content. Even scoped reads still return more structure than the write path usually needs.

3. The CLI hides an existing low-token server capability.
   The server already supports `full=false` on `/api/files/read`, but the CLI does not expose it. That means the interface forces heavier reads than the backend actually requires.

4. The skill documentation nudges Codex into eager reads.
   The current skill and CLI reference show workflows like `open --json`, full `read --json`, and full `threads list --json` early in the flow. That encourages the agent to load the whole document and every thread body before it knows whether that data is necessary.

5. The read-to-write handoff is too indirect.
   To edit safely, the model has to do some version of: `read` -> extract anchor -> write temp anchor file -> write temp text file -> `edit`. In the analyzed session that led to extra help calls, repeated quote reads, and shell glue.

6. Thread listing is eager and all-or-nothing.
   `threads list --json` returns every thread, every anchor, and every comment body. Often the agent only needs thread IDs, statuses, authors, counts, and maybe a small quote before deciding whether to inspect a specific thread.

7. `open` mixes side effects and data retrieval.
   The same command is used for opening the browser and for machine-readable document fetches. Those are different agent intents and should not share a heavyweight default payload.

8. The interface is not optimized around common agent tasks.
   Common Codex actions are: inspect a doc, locate a span, replace a span, create a thread on a span, list thread summaries, and open a deep link. The current surface is lower-level than those tasks, so the model compensates with extra steps.

9. The examples are shell-oriented rather than model-oriented.
   Temp files, `jq`, and multi-step composition are tolerable for humans, but they are a smell for an LLM interface. They raise both token cost and failure surface.

10. Comment highlight truthfulness is not guaranteed after edits.
   Today the system can show a thread highlight on text that is no longer the text that was commented on. For a collaboration product, stale-but-plausible highlights are worse than no highlight at all.

11. The Codex surface has no thread-aware edit path.
   Codex can edit anchored text and it can create threads, but it cannot clearly express higher-level intent like "replace the text for thread X and keep the comment attached to the replacement" or "if this rewrite invalidates the old quote, mark the thread unresolved rather than drifting." That makes comment stability depend on generic anchor repair instead of explicit workflow semantics.

## Target final shape

The best end state is a CLI-first interface that is sparse by default, supports one-shot write/comment flows, and only fetches rich document or thread payloads when the agent explicitly asks for them. It should also guarantee that thread highlights remain truthful after edits: either the thread anchor moves intentionally, or the thread is shown as unresolved with no misleading highlight.

### Workstream A: docs and examples should model the cheap path

1. Rewrite the AgentPad skill and reference docs to prefer a low-token workflow.
   Default review flow should be: health check, optional inspect/open, targeted `read`, optional thread summary read, then targeted thread action. Full document reads and full thread bodies should be explicitly opt-in.

2. Stop recommending `agentpad open <file> --json` as a general readiness step.
   Use the health endpoint for readiness. Use `open` when the user wants a browser link or when the agent truly needs the deep link.

3. Update examples so thread reading is progressive rather than mandatory.
   Codex should be free to inspect open threads early when that seems useful, but the default examples should start with summary or metadata shapes instead of eagerly loading every comment body.

4. Prefer stdin over temp files in examples where possible.
   The docs should model `--text-file -` and any future anchor stdin support, not `mktemp` plus `jq` by default.

5. Add guidance for comment-stable edits.
   The examples should distinguish between generic text edits and edits that are explicitly addressing a thread, because those should take different paths once the CLI supports thread-aware operations.

6. Update the skill to prefer localized edits by default.
   Unless the user explicitly wants a rewrite, the skill should tell Codex to edit the smallest affected span or block, preserve readable diffs, and avoid whole-document replacement when several smaller anchored edits would express the change more clearly.

   The examples should distinguish between generic text edits and edits that are explicitly addressing a thread, because those should take different paths once the CLI supports thread-aware operations.

### Workstream B: make the CLI sparse by default

1. Add `agentpad inspect <file> --json`.
   Return only small metadata such as path, URL, title, format, revision, and thread counts. This should become the machine-readable replacement for `open --json` when no full document payload is needed.

2. Change `open --json` to return a minimal shape by default.
   Suggested default: `{ path, url, revision, title, format }`.
   Add an explicit opt-in such as `--include-document` if full document data is still needed.

3. Expose the server's existing `full=false` capability in the CLI.
   This is not a text-search feature. It is a cheaper read shape that avoids returning the full document payload when the caller only needs a narrow span, anchor, or small amount of context.

4. Add output-shaping flags to `read`.
   Useful options: `--anchor-only`, `--text-only`, `--include-blocks`, `--context-chars`, or a generic `--fields` mechanism.

5. Stop returning both `text` and `blocks` by default for full reads.
   Pick one default representation and make the richer one opt-in.

6. Add summary mode for thread listing.
   `threads list --summary` should return only thread IDs, status, author, updated time, anchor quote, comment count, and whether the anchor is resolved.

7. Add a `threads get <file> <thread-id>` command.
   Full thread bodies should be lazy-loaded only when the agent actually wants to inspect or reply to a specific thread.

8. Surface anchor truth in CLI responses.
   Thread and read responses should clearly indicate whether an anchor is resolved, unresolved, or intentionally retargeted, so Codex does not treat stale positions as trustworthy.

### Workstream C: collapse the read-to-write workflow

1. Add `edit --anchor-stdin` or allow `--anchor-file -`.
   This removes one major reason for temp files and `jq` plumbing.

2. Make localized edits the default happy path.
   The easiest Codex flow should be "edit this exact span or block" rather than "replace the whole document." The interface should reward the smallest edit that fully expresses the requested change.

3. Add one-shot quote-native editing.
   Example shapes:
   - `agentpad edit <file> --quote "old" --text "new"`
   - `agentpad replace <file> --quote "old" --text-file -`
   The CLI should resolve the anchor internally so callers do not need an explicit pre-read step.

4. Fail closed on ambiguous quotes.
   If the quote matches multiple places, the command should not silently edit the first match. It should require disambiguation such as `--prefix`, `--suffix`, or `--block`.

5. Add multi-local-edit flows.
   Example shapes:
   - `agentpad edit-many <file> --edits-file edits.json`
   - `agentpad edit <file> --edits-json '[...]'`
   These should let Codex submit several localized edits in one operation instead of falling back to whole-document replacement when a task touches multiple disjoint sections.

6. Make the multi-local-edit format anchor-friendly.
   Each edit item should be able to target a quote, block, explicit anchor, or thread, and the command should apply them in document order with safe rebasing or reject the batch if the edits conflict.

7. Add one-shot quote-native thread creation.
   Example shape:
   - `agentpad threads create <file> --quote "text" --body "Comment"`
   The CLI should resolve the anchor internally instead of forcing the caller to convert quote intent into offsets first.

8. Add thread-aware edit flows.
   Example shapes:
   - `agentpad edit <file> --thread <thread-id> --text "replacement"`
   - `agentpad edit <file> --thread <thread-id> --text-file -`
   These flows should mean "I am editing the text this thread refers to" rather than "apply a generic text mutation somewhere nearby."

9. Define retargeting semantics for thread-aware edits.
   When a thread-aware edit replaces the exact commented span, the thread anchor should intentionally move to the replacement span instead of depending on quote repair. If the edit removes the span with no replacement, the thread should become explicitly unresolved.

10. Consider returning reusable anchors from more write operations.
   If an edit, multi-edit operation, or thread create returns the resolved or retargeted anchors it used, follow-up actions get cheaper and safer.

11. Add a compact `locate` command.
   Something like `agentpad locate <file> --quote ...` that returns only anchor metadata would be clearer than overloading `read` for both content retrieval and anchor lookup.

12. Let `threads create` accept anchors directly in the CLI.
   The server already supports a provided anchor. The CLI should expose `--anchor-json`, `--anchor-file`, or equivalent so Codex can use an exact anchor it just resolved instead of degrading to start/end offsets.
### Workstream D: keep comment highlights truthful after edits

1. Never return stale anchors as if they were resolved.
   If thread anchor resolution fails, `threads list` should not silently fall back to the old `doc_start` and `doc_end`. It should return an explicit unresolved state.

2. Persist rebased or retargeted thread anchors when appropriate.
   If a thread anchor is successfully rebased to the current document, the backend should consider updating the stored anchor so the same old anchor is not re-resolved from its original revision forever.

3. Refresh thread positions after document edits.
   Local edit acks and remote `op.applied` events should trigger thread refresh, or the server should include enough thread-anchor update information for the client to update them immediately.

4. Hide misleading highlights for unresolved threads.
   The UI should not paint a highlight at stale coordinates just because a thread still has historical offsets. Unresolved threads should remain visible in the sidebar with clear status, but not mark unrelated text in the editor.

5. Preserve the original quoted text separately from the current anchor target.
   A thread should be able to say both "this is the original text that was commented on" and "this is the current span we think it maps to" rather than conflating those into one mutable anchor blob.

6. Add explicit statuses or metadata for anchor health.
   Useful states are: resolved, retargeted, unresolved, and maybe stale-historical for diagnostics. Codex should be able to reason over those states directly.

### Workstream E: keep the Codex surface CLI-first

1. Keep the CLI as the primary interface.
   A thin Codex-facing wrapper may still be useful later, but it should be a small layer over the CLI/server, not a separate conceptual API surface.

2. Make heavy fields opt-in everywhere.
   Full blocks, full text, full comments, and activity streams should be behind explicit include flags.

3. Separate browser actions from machine reads.
   `open` should mean "open in browser". `inspect` should mean "give me structured metadata".

4. Design JSON responses for composability, not just completeness.
   A good default machine payload is the smallest object that allows the next action.

5. Make thread intent first-class in the interface.
   Codex should be able to express "read thread summary," "inspect thread anchor state," "edit the text for this thread," and "re-anchor this thread to the replacement text" without reconstructing that intent from generic text-edit primitives.

## Implementation note

These workstreams are grouped for clarity, not because they need to ship incrementally. If the scope is manageable, the better plan is to land the final CLI-first shape in one cohesive change set: lighter inspect/read/list defaults, one-shot edit/comment flows, truthful thread-anchor behavior after edits, updated examples, and a deterministic eval to prove the payoff.

## Acceptance criteria

- A standard review task should not need a full document payload unless the file is genuinely small and the user asked for a broad review.
- The first machine-readable document fetch should cost closer to metadata size, not thousands of tokens.
- The common localized replace flow should not require temp files.
- Multi-section changes should be expressible as readable localized edits rather than forcing whole-document replacement.

- The common comment flow should not require converting quote intent into offsets manually.
- Duplicate quotes should fail closed unless the caller disambiguates them.
- Thread inspection should be lazy by default, while still allowing Codex to inspect open threads early when it judges that to be useful.
- A thread highlight must never point at unrelated text after edits.
- If a thread can no longer be mapped confidently, it should become explicitly unresolved rather than silently drifting.
- Thread-aware edits should intentionally move the thread anchor to replacement text when that is the requested behavior.
- The skill examples should model the cheapest successful workflow, not the richest payload.

## Benchmark and eval plan

Use session `019d544a-ca2d-7432-8ed3-a53403d4d384` as the baseline regression case.

Target improvements:

- reduce the initial AgentPad bootstrap from roughly `9.5k` output tokens to under `2k`
- reduce the number of AgentPad reads needed before a comment/edit is made
- eliminate temp-file shell glue from the happy path
- make full thread bodies and full document blocks opt-in
- eliminate stale comment highlights after edits

Add a deterministic eval harness around those targets:

1. Create one or more fixed fixture docs with known comments, repeated quotes, common editing/review tasks, and thread-addressing edits.
2. Run a fixed prompt set against the same fixture state each time.
3. Record latency, token usage, command count, whether the agent chose sparse or heavyweight commands, and whether thread refreshes were needed after edits.
4. Grade correctness with a simple bar such as exact text edits, correct thread target, correct resolve/reply behavior, no silent first-match behavior on ambiguous quotes, and no stale highlight on unrelated text.
5. Add at least one fixture where the commented text is fully replaced and one where it is deleted.
6. Treat the eval as pass/fail plus scorecard rather than trying to optimize every metric perfectly.

## Low-risk opportunities already visible in the code

- The server already accepts `full=false` on the read endpoint.
- The server already accepts a provided anchor when creating a thread, even though the CLI does not expose that path yet.
- The domain package already has a `DocumentSummary` type that could back a lightweight inspect response.
- The current CLI already supports stdin for replacement text, so anchor stdin would fit the existing pattern.
- The existing quote, prefix, suffix, and block concepts already point toward a safe one-shot quote-native write flow.
- The current thread anchor model already has `resolved` and `resolved_block_id`, which can be extended into a more truthful anchor-health model instead of silently returning stale positions.

## Recommendation

Start with the CLI surface and skill examples, but include the comment-truthfulness fixes in the same final solution. The sessions suggest that we need both: lower-token defaults and a more explicit model for how comments survive edits. A cheaper interface that still returns stale thread anchors would be faster, but not trustworthy enough.
