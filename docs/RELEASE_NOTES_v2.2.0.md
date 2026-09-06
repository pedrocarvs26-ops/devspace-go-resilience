# Dev Space Go v2.2.0 — Linux amd64

Precompiled server package using the existing user-built binary, without rebuilding.

## Included
- Linux x86-64 server; dynamic glibc dependency >= 2.34.
- Example configuration, installation instructions and SHA-256 checksums.
- Async bash jobs with polling/cancellation and tunnel resilience changes.

## Validation
- Existing binary: `devspace help` succeeded on the preparation host.
- Source tests: PARTIAL: internal/config passed; the full core test command exceeded its 180-second job deadline. No full test-suite pass is claimed.
- Earlier Notion MCP smoke test: incremental stdout/stderr and successful completion.
- No heavy-load/soak test, race detection, Windows or macOS acceptance is claimed.
- Binary has no embedded Git revision. Source-to-binary correspondence is not reproducibly proven.

## Important
- This is NOT a static/musl/Alpine binary. No GUI/Windows/macOS binaries are included.
- Personal configs, workspace state, credentials and third-party tunnel binaries are excluded.
- Install cloudflared or OpenSSH separately if remote access is needed.
- Hard CPU/memory/bandwidth limits require explicit configuration and platform support.
- Pinggy Free expires after 60 minutes; Quick Tunnel URLs may change and Quick Tunnels do not support SSE.
- Use `/mcp` with JSON/polling. Protect this remote shell endpoint; URL secrecy is not authentication.
- Recommend publishing as a GitHub prerelease until target-host acceptance is complete.
