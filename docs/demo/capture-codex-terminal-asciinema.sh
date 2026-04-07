#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

READY_MARKER="/tmp/agentpad-human-comment-created"
CAST_OUT="docs/videos/agentpad-codex-terminal.cast"
GIF_OUT="docs/videos/agentpad-codex-terminal.gif"
SOCK="agentpad-demo-$$"
WINDOW_SIZE="160x24"
CODEX_COMMAND="env AGENTPAD_CONFIG=$ROOT/docs/demo/agentpad.demo.toml codex --dangerously-bypass-approvals-and-sandbox --no-alt-screen -m gpt-5.4-mini -c model_reasoning_effort=low -C $ROOT"
PROMPT="Use \$agentpad. Follow docs/demo/codex-agent-prompt.md exactly to inspect the human review thread, update the plan, and reply in AgentPad."
CODEX_READY_PATTERN="OpenAI Codex"
COMPLETION_MARKERS=(
  "Updated the Goal section in docs/demo/coding-agent-plan.md and replied on the existing human review thread in AgentPad."
  "I added the rollout KPI and rollback threshold to the Goal section."
  "What I changed:"
  "Completed on "
  "No errors from the AgentPad server or CLI."
)

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

ensure_agg() {
  if [[ -n "${AGG_BIN:-}" && -x "${AGG_BIN:-}" ]]; then
    printf '%s\n' "$AGG_BIN"
    return
  fi

  if command -v agg >/dev/null 2>&1; then
    command -v agg
    return
  fi

  local bin_dir="$ROOT/docs/demo/.bin"
  local bin_path="$bin_dir/agg"
  local url

  if [[ -x "$bin_path" ]]; then
    printf '%s\n' "$bin_path"
    return
  fi

  mkdir -p "$bin_dir"

  case "$(uname -s)-$(uname -m)" in
    Darwin-arm64)
      url="https://github.com/asciinema/agg/releases/download/v1.7.0/agg-aarch64-apple-darwin"
      ;;
    Darwin-x86_64)
      url="https://github.com/asciinema/agg/releases/download/v1.7.0/agg-x86_64-apple-darwin"
      ;;
    Linux-aarch64|Linux-arm64)
      url="https://github.com/asciinema/agg/releases/download/v1.7.0/agg-aarch64-unknown-linux-gnu"
      ;;
    Linux-x86_64)
      url="https://github.com/asciinema/agg/releases/download/v1.7.0/agg-x86_64-unknown-linux-gnu"
      ;;
    *)
      echo "Unsupported platform for agg bootstrap: $(uname -s)-$(uname -m)" >&2
      exit 1
      ;;
  esac

  curl -fL "$url" -o "$bin_path"
  chmod +x "$bin_path"
  printf '%s\n' "$bin_path"
}

cleanup() {
  tmux -L "$SOCK" kill-server >/dev/null 2>&1 || true
}
trap cleanup EXIT

require_command asciinema
require_command tmux
require_command curl

AGG_PATH="$(ensure_agg)"

mkdir -p docs/videos
rm -f "$CAST_OUT" "$GIF_OUT"
export PATH="$ROOT/docs/demo:$PATH"

cat > /tmp/agentpad-asciinema-driver.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

SOCK="$1"
PROMPT="$2"
COMPLETION_MARKERS=(
  "Updated the Goal section in docs/demo/coding-agent-plan.md and replied on the existing human review thread in AgentPad."
  "I added the rollout KPI and rollback threshold to the Goal section."
  "What I changed:"
  "Completed on "
  "No errors from the AgentPad server or CLI."
)

for _ in $(seq 1 100); do
  if tmux -L "$SOCK" has-session -t demo >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

if ! tmux -L "$SOCK" has-session -t demo >/dev/null 2>&1; then
  echo "Timed out waiting for tmux session demo" >&2
  exit 1
fi

sleep 1
tmux -L "$SOCK" send-keys -t demo:0.0 -l "$PROMPT"
sleep 1
tmux -L "$SOCK" send-keys -t demo:0.0 Enter

for _ in $(seq 1 900); do
  pane="$(tmux -L "$SOCK" capture-pane -pt demo:0.0 || true)"
  for marker in "${COMPLETION_MARKERS[@]}"; do
    if [[ "$pane" == *"$marker"* ]]; then
      sleep 4
      break 2
    fi
  done
  sleep 0.1
done

tmux -L "$SOCK" kill-session -t demo
EOF
chmod +x /tmp/agentpad-asciinema-driver.sh

tmux -L "$SOCK" kill-server >/dev/null 2>&1 || true
tmux -L "$SOCK" -f /dev/null new-session -d -s demo -c "$ROOT" "$CODEX_COMMAND"

for _ in $(seq 1 200); do
  pane="$(tmux -L "$SOCK" capture-pane -pt demo:0.0 || true)"
  if [[ "$pane" == *"$CODEX_READY_PATTERN"* ]]; then
    break
  fi
  sleep 0.1
done

if [[ "$pane" != *"$CODEX_READY_PATTERN"* ]]; then
  echo "Timed out waiting for Codex session to become visible" >&2
  exit 1
fi

tmux -L "$SOCK" send-keys -t demo:0.0 C-l
sleep 0.2
tmux -L "$SOCK" clear-history -t demo:0.0

for _ in $(seq 1 1200); do
  if [[ -f "$READY_MARKER" ]]; then
    break
  fi
  sleep 0.1
done

if [[ ! -f "$READY_MARKER" ]]; then
  echo "Timed out waiting for browser ready marker: $READY_MARKER" >&2
  exit 1
fi

/tmp/agentpad-asciinema-driver.sh "$SOCK" "$PROMPT" &
DRIVER_PID=$!

asciinema rec \
  --headless \
  --overwrite \
  --window-size "$WINDOW_SIZE" \
  --command "env TERM=xterm-256color tmux -L $SOCK attach-session -t demo" \
  "$CAST_OUT"

wait "$DRIVER_PID"

"$AGG_PATH" \
  --font-size 22 \
  --theme github-dark \
  --idle-time-limit 120 \
  --last-frame-duration 0.1 \
  "$CAST_OUT" \
  "$GIF_OUT"
