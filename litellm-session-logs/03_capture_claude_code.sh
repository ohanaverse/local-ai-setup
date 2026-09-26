#!/bin/sh
# Snapshot ("capture") one Claude Code session's raw local record from
# ~/.claude/projects into a stable directory, for build_cc_transcript.py
# to build a full-context Markdown transcript from.
#
# Why copy instead of reading in place: ~/.claude applies retention
# cleanup (default 30 days) that can delete the raw record, and a live
# session keeps appending to these same files.
#
# Copies:
#   <snapshot>/<session-id>.jsonl   the main session transcript
#   <snapshot>/<session-id>/        the session dir (subagents/ etc.), if present
#   <snapshot>/capture.json         manifest: source dir + capture time
#
# Usage: ./03_capture_claude_code.sh <session-id> [snapshot-dir]
#   snapshot-dir defaults to session_cc_<session-id> in the current directory.
#   CLAUDE_PROJECTS_DIR overrides the projects root (default: $HOME/.claude/projects).

set -eu

SESSION_ID="${1:?usage: $0 <session-id> [snapshot-dir]}"
SNAPSHOT_DIR="${2:-session_cc_${SESSION_ID}}"
PROJECTS_DIR="${CLAUDE_PROJECTS_DIR:-$HOME/.claude/projects}"

if [ ! -d "$PROJECTS_DIR" ]; then
  echo "error: $PROJECTS_DIR does not exist" >&2
  exit 1
fi

# Locate the session's main transcript: ~/.claude/projects/<slug>/<session-id>.jsonl
# (project slugs replace every character outside [a-zA-Z0-9-] with '-', so the
# paths below never contain whitespace — word-splitting in the for loop is safe)
matches=$(find "$PROJECTS_DIR" -maxdepth 2 -name "${SESSION_ID}.jsonl" 2>/dev/null || true)
if [ -z "$matches" ]; then
  echo "error: no ${SESSION_ID}.jsonl under $PROJECTS_DIR" >&2
  echo "hint: the session may not exist, or was run with persistence disabled" >&2
  exit 1
fi

main_file=$matches
match_count=$(printf '%s\n' "$matches" | grep -c .)
if [ "$match_count" -gt 1 ]; then
  echo "warning: multiple matches for ${SESSION_ID}:" >&2
  printf '%s\n' "$matches" >&2
  main_file=
  newest=0
  for f in $matches; do
    mtime=$(stat -f %m "$f") # macOS stat; this repo is macOS-only (LaunchAgents, flock)
    if [ "$mtime" -gt "$newest" ]; then
      newest=$mtime
      main_file=$f
    fi
  done
  echo "warning: using newest by mtime: $main_file" >&2
fi

slug_dir=$(dirname "$main_file")

mkdir -p "$SNAPSHOT_DIR"
cp -Rp "$main_file" "$SNAPSHOT_DIR/"
if [ -d "${slug_dir}/${SESSION_ID}" ]; then
  cp -Rp "${slug_dir}/${SESSION_ID}" "$SNAPSHOT_DIR/"
fi

printf '{"session_id":"%s","source_dir":"%s","captured_at":"%s"}\n' \
  "$SESSION_ID" "$slug_dir" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  > "${SNAPSHOT_DIR}/capture.json"

echo "captured session ${SESSION_ID} from ${slug_dir} into ${SNAPSHOT_DIR}/"
ls -lR "$SNAPSHOT_DIR"
