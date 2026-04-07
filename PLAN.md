# AgentPad Plan

## Goals

- Make the CLI and skill workflow feel agent-native and low-friction.

## Current Focus

- Align the Codex skill guidance with the current CLI surface.

## Near-Term Work

1. Make `agentpad agent-usage` the canonical LLM-facing command.
2. Keep the installed skill minimal: a strong description for auto-selection plus a tiny pointer that tells Codex to run `agentpad agent-usage`.
3. Move volatile operational guidance into CLI-owned usage output.
4. Have `install-skill` install that minimal pointer skill from the current binary.

## Decisions

- The canonical LLM-facing command is `agentpad agent-usage`.
- The skill should stay tiny and should primarily help Codex know when to invoke AgentPad.
- `install-skill` should install the minimal pointer skill from the current binary.


## Definition Of Done

- A new user can install AgentPad, invoke the skill, and get the right workflow without extra prompting.
- Updating the binary updates the canonical usage guidance.
