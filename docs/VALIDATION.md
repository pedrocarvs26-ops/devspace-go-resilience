# Delivery validation — resilience patch

## Status

**Implementation candidate; NOT a compiled or production-accepted release.**
The supplied ZIP was used as the baseline. No GitHub branch, commit, PR, running
server, machine-wide setting or real tunnel was modified.

## Checks actually executed in the delivery sandbox

- Python syntax compilation for the acceptance harness and fixture generator: passed.
- `bash -n` for the network-provisioning and verification scripts: passed.
- `git diff --check`: passed before packaging.
- `python3 scripts/acceptance/test_harness.py`: **3 tests passed**.
  - Artificial **1 MiB** Git fixture generated and cloned through a local HTTP server.
  - Load execution requires explicit confirmation.
  - MCP driver polling/success/error/cancellation behavior tested against a **mock**
    MCP server. Mock acceptance reports were temporary and are NOT real load results.
- Source/patch archive round-trip and file integrity are checked during packaging.

## Checks NOT executed / not established

- `go test`, `go vet`, `gofmt`, race detection and Go cross-compilation could not run:
  **`go: command not found`**. Network requests to obtain Go failed DNS resolution.
  The repository already declares **Go 1.26.2** in `go.mod`; that was not downgraded.
- Go code has been reviewed, but its compilation, dependency/API compatibility and
  runtime behavior remain unverified. Run `scripts/unix/verify-resilience.sh` before
  deployment; it explicitly formats the Go files as well as testing/building them.
- No >500 MiB acceptance workload was run through a real Cloudflare/Pinggy tunnel.
- No real cgroup/systemd delegation, network namespace/tc policy, Windows Job Object
  CPU/memory/network enforcement or macOS execution was tested in this sandbox.
- No claim is made of zero disconnects, uninterrupted sessions, universal bandwidth
  enforcement, or successful production acceptance.

## Required target-host validation

1. Review the patch and run `./scripts/unix/verify-resilience.sh` on an appropriate
   development host. Run native Windows tests as well; cross-compilation alone is
   not runtime validation. Test the GUI natively if you ship it.
2. Start with a disposable allowed workspace. Test async success, nonzero exit,
   deadline, cancellation, descendants, reconnect and bounded-output cursors.
3. Enable hard limits on the intended deployment. Verify actual cgroup or Job Object
   metrics while jobs run. Test missing/unsupported controls: user commands must not
   run outside requested quotas. Confirm quotas leave enough total CPU/RAM headroom.
4. On Linux, provision bandwidth isolation only after administrator review. Check
   both veth directions and namespace routing/DNS. Do not run the MCP server as root.
   On Windows, separately enforce/measure inbound bandwidth: the implemented Job
   Object network policy caps outbound traffic only.
5. Use the README acceptance harness with a generated **544 MiB incompressible**
   repository, a separate load generator, CPU workers and a real public tunnel.
   Record the configured latency threshold, resource usage, JSON result, shell
   output log, and timestamped devspace/provider logs. Repeat for each supported
   provider and deployment OS; perform a soak test appropriate to the workload.
6. Separately inject a tunnel-process exit and an unreachable public endpoint.
   Confirm interruptible exponential backoff, recovery, continued polling of the
   same job and no restart after server shutdown. A named tunnel is required when
   clients must keep using the same URL. Induced failures are not zero-drop passes.

## Compatibility and limits worth reviewing

- Existing JSON configs retain defaults, but shell **response semantics change**:
  clients now receive a job ID and must poll `bash_status`.
- Hard resource quotas are opt-in; default priority/concurrency/output limits alone
  cannot guarantee headroom against every workload or other host processes.
- Output is incremental through polling, not a new SSE output endpoint. Bounded
  retention can drop old output; dropped bytes are reported explicitly.
- Jobs survive HTTP disconnects, not a full devspace process restart.
- Quick Tunnel/Pinggy addresses may change. No claim of a permanent Pinggy address
  based solely on an SSH key is retained in the main README.
- The existing lack of built-in MCP authentication remains. Resource guardrails are
  not a sandbox against malicious commands and do not replace endpoint access control.
- README translations were not synchronized; the main README is authoritative for
  these changes. The GUI preserves new settings when saving but adds no new controls.

## Follow-up release preparation — 2026-09-06

An existing Linux amd64 binary was inspected on the user host and `devspace help` succeeded.
Source tests: PARTIAL: internal/config passed; the full core test command exceeded its 180-second job deadline. No full test-suite pass is claimed.
Sources were formatted with gofmt for publication. The existing binary was NOT rebuilt or replaced.
The original sandbox notes above remain historical; see RELEASE_NOTES_v2.2.0.md for current distribution limits.
