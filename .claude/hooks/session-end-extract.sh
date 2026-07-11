#!/usr/bin/env bash
# SessionEnd hook: headless knowledge extraction into the daily log.
# Reads the Claude Code hook payload on stdin, then runs `claude -p` over the
# session transcript to append categorized entries to knowledge-base/daily/.
# Writes ONLY to daily/ (raw-capture tier) — graduation into knowledge/ or
# projects/ stays a deliberate /kb-compile act.
# Must always exit 0: a broken extractor must never make session exit noisy.

set -u
exec 2>/dev/null

ROOT="${CLAUDE_PROJECT_DIR:-$(pwd)}"
PAYLOAD="$(cat)"

read -r TRANSCRIPT REASON <<EOF
$(printf '%s' "$PAYLOAD" | python3 -c '
import json,sys
try:
    d = json.load(sys.stdin)
    print(d.get("transcript_path",""), d.get("reason",""))
except Exception:
    print("", "")
')
EOF

# Guards: only extract from substantive, genuinely-ended sessions.
[ -n "${TRANSCRIPT:-}" ] && [ -f "$TRANSCRIPT" ] || exit 0
case "${REASON:-}" in clear|other) exit 0 ;; esac
[ "$(wc -c < "$TRANSCRIPT")" -ge 20000 ] || exit 0
command -v claude >/dev/null || exit 0

# The parent session injects harness-specific API routing that breaks a nested
# headless run; drop it so `claude -p` authenticates with its own credentials.
# (Requires a one-time `claude /login` on this machine for headless use.)
unset ANTHROPIC_BASE_URL ANTHROPIC_API_KEY CLAUDECODE CLAUDE_CODE_ENTRYPOINT CLAUDE_CODE_SESSION_ID

# Serialize concurrent session ends appending to the same daily log. A lock
# left behind by a crashed run (trap never fired) must not disable extraction
# forever: treat a lock older than 30 minutes as stale and take it over.
LOCK="$ROOT/.claude/hooks/.extract.lock"
if ! mkdir "$LOCK" 2>/dev/null; then
  find "$LOCK" -maxdepth 0 -type d -mmin +30 -exec rmdir {} \; 2>/dev/null
  mkdir "$LOCK" 2>/dev/null || exit 0
fi
trap 'rmdir "$LOCK"' EXIT

TODAY="$(date +%F)"
NOW="$(date '+%Y-%m-%d %H:%M:%S')"
NOWTIME="$(date '+%H:%M:%S')"
DAILY="knowledge-base/daily/$TODAY.md"

cd "$ROOT" || exit 0

claude -p --model haiku --allowedTools "Read Write Edit Bash(uuidgen:*)" -- "
You are the knowledge extractor for this silo. Read the session transcript at
$TRANSCRIPT (it is JSONL; skim user/assistant messages, skip tool noise).

Distill durable knowledge from the session and append it to $DAILY. The daily
log is TIME-PRIMARY: each capture pass is a '## HH:MM:SS' block, and the
categories are '###' subsections inside it.
- If the file does not exist, create it with YAML frontmatter: a fresh lowercase
  UUID as 'id' (run uuidgen), 'type: daily-log', 'title: $TODAY',
  'timestamp: $NOW', then an h1 '# $TODAY'.
- The file may ALREADY contain '## <time>' blocks the agent captured in-session.
  Read it first and only ADD durable items that are genuinely missing — never
  duplicate or reword an entry already present under any block. If everything
  durable is already captured, write nothing and finish.
- Append your findings as ONE new block headed '## $NOWTIME'. Inside it, add
  only the '###' category subsections you have entries for:
  '### Concepts', '### Cursed Knowledge', '### Unresolved', '### Log'.
- Concepts: durable ideas/decisions worth remembering. Cursed Knowledge:
  surprising gotchas. Unresolved: open questions as '- [ ]' checkboxes.
  Log: one-line summary of what the session did.
- Categorized bullets only — no freeform prose outside subsections. Never edit
  any file other than $DAILY. If the session contained nothing durable, write
  nothing and finish.
" >/dev/null

exit 0
