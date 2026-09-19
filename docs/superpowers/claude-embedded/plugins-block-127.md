<!--
  Extracted from Claude Code v2.1.278
  Source offset: 193508114
  Content hash: fbc37eb2ec952dd4
  Category: plugins
  Auto-generated — do not edit manually
-->

Applies to **both** Cowork and Code, and only to **tool calls**. In Cowork it governs the sandbox's web fetch, shell commands, and package installs; in Code sessions it is [translated into Claude Code's network sandbox allowlist](/code#applied-as-managed-policy), where a separately deployed Claude Code managed-settings file takes precedence by default. It does **not** cover Web Search (which runs at your inference provider), inference, or MCP traffic. When unset, only the inference endpoint is reachable from the sandbox, so the agent's package installs and web fetches fail with a 403.

Entries are exact hostnames (`api.github.com`), wildcards (`*.corp.com` matches subdomains at any depth, not `corp.com` itself), or `*` to allow all. IP addresses match only when listed exactly. `localhost` and private-network addresses are always blocked for web fetch; shell commands and package installs run in a network sandbox that reaches only the listed hosts plus your inference provider. With `*`, that sandbox is disabled and web fetch still blocks private addresses.

Any entry except bare `*` may carry a `:port` suffix (`internal.corp.com:8443`, `*.corp.com:8443`) restricting it to that port. IPv6 literals are not supported. An invalid entry is dropped (with a warning in the app log) and the rest keep working. Ports are enforced for the Cowork sandbox's web fetch, shell, and package-install egress; plugin CLIs ignore port-scoped entries for now, and the Code translation treats them as the bare host. Deploy port-scoped entries only once your whole fleet is on a build that supports them (`disableAutoUpdates` pins builds): on an older build one such entry invalidates the sandbox's whole shell and package-install allowlist for the session.

Listed hosts also need to be open on your network firewall.