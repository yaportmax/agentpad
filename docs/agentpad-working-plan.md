# AgentPad Working Plan

## Goal

Make AgentPad's Codex workflow current, low-token, and trustworthy for human-plus-agent collaboration.

## Immediate Workstreams

### 1. Keep agent instructions in sync

- Keep the skill description strong enough for automatic routing.
- Move detailed operational guidance toward a CLI-owned usage surface.
- Keep the installed skill thin so binary updates do not leave long instructions stale.
- Add a version or status check that can detect when skill expectations drift from the current CLI.

### 2. Keep the CLI sparse by default

- Use lightweight document inspection by default.
- Keep rich document payloads opt-in rather than eager.
- Prefer narrow reads and summary thread views before loading full content.
- Preserve machine-readable shapes that compose cleanly for agents.

### 3. Keep thread highlights truthful after edits

- Surface resolved, retargeted, and unresolved anchor states explicitly.
- Avoid returning stale coordinates as if they were current.
- Refresh thread state after document edits.
- Prefer thread-aware edit flows when an agent is addressing an existing comment.

### 4. Improve agent ergonomics

- Reduce shell glue and temp-file steps in common edit flows.
- Prefer examples that model the cheapest successful path.
- Keep localized edits as the default path unless the user wants a rewrite.
- Verify that docs, skill examples, and command behavior stay aligned during the current refactor.

## Suggested Sequence

1. Lock the intended CLI surface and output shapes.
2. Update the skill and references to match the current command surface.
3. Add or refresh tests for lightweight reads, summary thread listing, thread fetch, and thread-aware edits.
4. Run an end-to-end Codex session and compare token cost, command count, and thread correctness against the old flow.

## Risks

- The in-flight refactor can leave examples and behavior temporarily mismatched.
- Thread-truthfulness fixes may require coordinated backend, CLI, and frontend changes.
- Defaulting to smaller payloads can break scripts or demos that depended on heavier responses.

## Open Questions

- Should a CLI usage command become the canonical source of detailed Codex instructions?
- Should `install-skill` install a thin pointer skill, a generated skill, or both?
- Which current command behaviors need compatibility shims during the transition?
