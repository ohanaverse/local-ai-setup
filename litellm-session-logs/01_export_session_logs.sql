-- Full request/response bodies for one LiteLLM proxy session.
--
-- `messages` will be `{}` for every row unless the call type is realtime
-- (`_arealtime`) — for standard chat completions, use
-- 02_export_proxy_server_request.sh instead to get the real request body.
--
-- Usage (LITELLM_DATABASE_URL overrides the connection string, as in
-- 02_export_proxy_server_request.sh):
--   psql "${LITELLM_DATABASE_URL:-postgresql://keith@localhost:5432/litellm}" \
--     -v session_id="'<session-id>'" \
--     -f 01_export_session_logs.sql

SELECT
  request_id,
  "startTime",
  status,
  request_duration_ms,
  prompt_tokens,
  completion_tokens,
  spend,
  messages,
  response
FROM "LiteLLM_SpendLogs"
WHERE session_id = :session_id
ORDER BY "startTime";
