#!/usr/bin/env bash
# Run one Codex turn that drives the sactl client, so "can the model play?"
# stays a repeatable measurement instead of an opinion.
#
# It uses an isolated CODEX_HOME under runtime/ so the operator's own
# ~/.codex/config.toml is never read or modified, and it never puts a model
# key on a command line: the key comes from the copied auth.json.
#
# Usage:
#   scripts/sactl-agent-smoke.sh [prompt-file]
#
# Prerequisites (see docs/sactl.md):
#   - build/local/sactl built and `sactl serve --config runtime/sactl.toml` running
#   - ~/.codex/config.toml + auth.json present for the model provider
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Codex state lives outside the repository: it writes directory modes that are
# not traversable, which breaks `go build ./...` when it sits inside the tree.
codex_home="${SACTL_CODEX_HOME:-${XDG_STATE_HOME:-$HOME/.local/state}/sactl/codex-home}"
workspace="$root/runtime/sactl-agent-workspace"
socket="$root/runtime/sactl/sactl.sock"
prompt_file="${1:-}"

if [[ ! -x "$root/build/local/sactl" ]]; then
  echo "sactl-agent-smoke: build it first: go build -mod=mod -o build/local/sactl ./cmd/sactl" >&2
  exit 1
fi
if [[ ! -S "$socket" ]]; then
  echo "sactl-agent-smoke: no daemon on $socket; start: ./build/local/sactl serve --config runtime/sactl.toml" >&2
  exit 1
fi

# Isolated Codex home: copy the operator's provider config, never edit it.
mkdir -p "$codex_home" "$workspace/bin" "$workspace/.agents/skills"
cp "$HOME/.codex/config.toml" "$codex_home/config.toml"
cp "$HOME/.codex/auth.json" "$codex_home/auth.json"
chmod 700 "$codex_home"
# Only the copied files are secrets. A blanket chmod would strip the execute
# bit from Codex's own state directories and lock it out of its own home.
find "$codex_home" -maxdepth 1 -type f -exec chmod 600 {} +
cp -R "$root/ai/skills/stoneage-cli" "$workspace/.agents/skills/"

# A wrapper keeps the socket path stable no matter where the agent runs from.
cat > "$workspace/bin/sactl" <<EOF
#!/bin/sh
exec "$root/build/local/sactl" --socket "$socket" "\$@"
EOF
chmod +x "$workspace/bin/sactl"

default_prompt='你是石器时代 2.5 里的 AI 玩家。用 sactl 自己安排这一轮：先观察你在哪、周围有什么，
自己定目标并完成它，每步之后核对世界是否如你预期，最后报告做了什么、服务端如何响应、现在在哪、下一步打算。
只使用 sactl 命令，不要修改任何文件。'
prompt="$default_prompt"
if [[ -n "$prompt_file" ]]; then
  prompt="$(cat "$prompt_file")"
fi
if [[ -z "${prompt//[[:space:]]/}" ]]; then
  prompt="$default_prompt"
fi

CODEX_HOME="$codex_home" PATH="$workspace/bin:$PATH" \
  codex exec --json --skip-git-repo-check -C "$workspace" "$prompt" < /dev/null
