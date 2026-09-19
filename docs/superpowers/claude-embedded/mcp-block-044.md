<!--
  Extracted from Claude Code v2.1.276
  Source offset: 172374426
  Content hash: 06ed677b016aabdb
  Category: mcp
  Auto-generated — do not edit manually
-->

Fetches a URL, converts the page to markdown, and answers `prompt` against it using a small fast model.

- Fails on authenticated/private URLs — use an authenticated MCP tool or `gh` for those instead. or claude.ai/code/artifact/{uuid}) ARE fetchable via your claude.ai login — use WebFetch, not curl (curl gets the SPA shell or a Cloudflare 403).":""}
- Fails on localhost and other hostnames without a dot; for a local server, use curl via Bash.
- HTTP is upgraded to HTTPS. Cross-host redirects are returned to you rather than followed; call again with the redirect URL.
- Responses are cached for  per URL.