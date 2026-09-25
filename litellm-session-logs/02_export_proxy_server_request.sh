#!/bin/sh
# Dump proxy_server_request (the real request body — messages, system prompt,
# tool results) for one LiteLLM session to newline-delimited JSON, one row
# per line, for build_full_transcript.py to consume.
#
# Only useful if `store_prompts_in_spend_logs` was on at capture time —
# otherwise this column is `{}` too, same as `messages`.
#
# Uses CSV format with control-character quote/delimiter chars for \copy:
# COPY's default TEXT format re-escapes backslashes inside the JSON text
# (turning JSON's `\n` into `\\n`), which breaks json.loads on read. CSV
# format only needs to escape the quote character, so this avoids that.
#
# Usage: ./02_export_proxy_server_request.sh <session_id> [output_file]

set -eu

SESSION_ID="${1:?usage: $0 <session_id> [output_file]}"
OUT="${2:-session_${SESSION_ID%%-*}_proxy_requests.ndjson}"
DB_URL="${LITELLM_DATABASE_URL:-postgresql://keith@localhost:5432/litellm}"

psql "$DB_URL" -q -c "\copy (
  SELECT row_to_json(t)
  FROM (
    SELECT request_id, \"startTime\", status, proxy_server_request
    FROM \"LiteLLM_SpendLogs\"
    WHERE session_id = '${SESSION_ID}'
    ORDER BY \"startTime\"
  ) t
) TO '${OUT}' WITH (FORMAT csv, QUOTE E'\x01', DELIMITER E'\x02')"

echo "wrote $OUT"
wc -l "$OUT"
