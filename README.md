# Dev Space Go

**Give ChatGPT & Claude secure access to your local machine. Turn any MCP host into your coding partner.**

Dev Space Go is a self-hosted MCP server that lets AI assistants read, edit, search, and run code in your real local projects — your files, your tools, your terminal — without uploading anything to a third party. You run it on your machine and expose it through a tunnel you control.

---

## Table of Contents

- [Quick Start](#quick-start)
- [Installation](#installation)
- [What AI Can Do](#what-ai-can-do)
- [Configuration](#configuration)
- [Tunnel (Remote Access)](#tunnel-remote-access)
- [Shell Support](#shell-support)
- [Security](#security)
- [Building from Source](#building-from-source)
- [Platform Support](#platform-support)
- [Project Structure](#project-structure)

### 🌍 Translations

| Language | | Language | | Language | |
|---|---|---|---|---|---|
| [Afrikaans](readme/af.md) | [العربية](readme/ar.md) | [Български](readme/bg.md) | [বাংলা](readme/bn.md) | [Català](readme/ca.md) |
| [Čeština](readme/cs.md) | [Dansk](readme/da.md) | [Deutsch](readme/de.md) | [Ελληνικά](readme/el.md) | [English](readme/en.md) |
| [Español](readme/es.md) | [Eesti](readme/et.md) | [فارسی](readme/fa.md) | [Suomi](readme/fi.md) | [Français](readme/fr.md) |
| [Gaeilge](readme/ga.md) | [עברית](readme/he.md) | [हिन्दी](readme/hi.md) | [Hrvatski](readme/hr.md) | [Magyar](readme/hu.md) |
| [Indonesia](readme/id.md) | [Italiano](readme/it.md) | [日本語](readme/ja.md) | [한국어](readme/ko.md) | [Lietuvių](readme/lt.md) |
| [Latviešu](readme/lv.md) | [Melayu](readme/ms.md) | [Malti](readme/mt.md) | [Nederlands](readme/nl.md) | [Norsk](readme/no.md) |
| [Polski](readme/pl.md) | [Português](readme/pt.md) | [Română](readme/ro.md) | [Русский](readme/ru.md) | [Slovenčina](readme/sk.md) |
| [Slovenščina](readme/sl.md) | [Српски](readme/sr.md) | [Svenska](readme/sv.md) | [Kiswahili](readme/sw.md) | [தமிழ்](readme/ta.md) |
| [ไทย](readme/th.md) | [Türkçe](readme/tr.md) | [Українська](readme/uk.md) | [اردو](readme/ur.md) | [Tiếng Việt](readme/vi.md) |
| [简体中文](readme/zh.md) | [isiZulu](readme/zu.md) |

---

## Quick Start

### 1. Download
Pick your platform from [Releases](../../releases) or build from source:
```bash
./scripts/unix/build.sh      # Linux / Mac
.\scripts\windows\build.ps1   # Windows
```

### 2. Configure (GUI or text)
```bash
devspace-gui                  # Desktop configurator (GUI)
devspace init                 # Text-based configurator
```

### 3. Run
```bash
devspace                      # Starts server. Auto-detects config.
```

This also auto-starts a Cloudflare Tunnel if `cloudflared` is found in `tools/`.

### 4. Connect your MCP client
```
https://YOUR-TUNNEL.trycloudflare.com/mcp
```
Or locally: `http://127.0.0.1:7676/mcp`

---

## Installation

No Node.js, no npm, no Python. Single binary.

| Platform | Download |
|---|---|
| **Windows** | `devspace.exe` + `devspace-gui.exe` |
| **Linux** | `devspace` (GUI: compile natively) |
| **macOS Intel** | `devspace` (GUI: compile natively) |
| **macOS M-chip** | `devspace` (GUI: compile natively) |

Requires **Go 1.26.2+** (see `go.mod`) if building from source.

---

## What AI Can Do

Once connected, the AI can open one of your approved project folders as a workspace:

- **Read, write, and edit** files inside the workspace
- **Create directories and move/rename files** safely inside the workspace
- **Search code** with regex and inspect directories
- **Run shell commands** (PowerShell on Windows, bash on Unix)
- **Discover project instructions** from `AGENTS.md` / `CLAUDE.md`
- **Auto-configure** with portable `.devspace/config.json`

MCP tools include `open_workspace`, `open_default_workspace`, `read`, `write`, `mkdir`, `move`, `edit`, `grep`, `glob`, `ls`, `bash`, `bash_status`, and `bash_cancel`.

---

## Configuration

All config lives **in the same folder as the executable** (portable):

```
.devspace/
└── config.json       ← allowed roots, port, shell, language
```

### config.json
```json
{
  "host": "127.0.0.1",
  "port": 7676,
  "allowedRoots": ["C:/projects"],
  "publicBaseUrl": "http://127.0.0.1:7676",
  "shell": "auto",
  "lang": "auto",
  "toolMode": "full",
  "toolNaming": "short"
}
```

| Field | Default | Description |
|---|---|---|
| `shell` | `auto` | `auto`, `powershell`, `cmd`, `bash`, `sh` |
| `lang` | `auto` | Auto-detect from OS. Supports 47 languages |
| `toolMode` | `full` | `full` (all tools) or `minimal` (shell only for search) |
| `toolNaming` | `short` | `short` (read, write) or `legacy` (read_file, write_file) |

No environment variables needed — everything is in the portable config file.

### Long-running shell jobs and tunnel resilience

Shell calls (`bash`, or `run_shell` with legacy naming) now **return immediately**
with a `job_id`. They do not keep an HTTP request open while Git/builds run.
`bash_status` and `bash_cancel` are available in both full/minimal and short/legacy
modes. The existing input fields remain accepted; **the response contract changes
from a final string to a job acknowledgment**. Clients must poll instead of treating
`result` from the first call as completed command output. Old config files still
load; omitted fields and nested settings retain defaults. CLI and GUI saves preserve
these settings. Invalid configurations fail at server startup rather than silently
turning off resource protection.

```json
{
  "httpReadTimeout": "15s",
  "httpWriteTimeout": "0s",
  "httpIdleTimeout": "120s",
  "tunnelHeartbeatInterval": "15s",
  "tunnelReconnectBackoff": "1s",
  "bashJobs": {
    "maxConcurrent": 2,
    "maxRetained": 64,
    "outputBytes": 1048576,
    "timeout": "1h",
    "maxTimeout": "24h",
    "retention": "30m"
  },
  "bashResourceLimit": {
    "enabled": false,
    "cpuPercent": 25,
    "memoryMB": 1024,
    "bandwidthBytesPerSecond": 0,
    "nice": 10,
    "systemdUser": true
  }
}
```

Durations use Go duration strings, e.g. `500ms`, `15s`, `2m`, `1h` (not raw numeric
nanoseconds). `httpReadTimeout` bounds reading headers/body, not job lifetime.
`httpWriteTimeout: "0s"` deliberately disables the absolute write deadline because
`/sse` is a long-lived legacy transport; a finite value can disconnect SSE clients.
`httpIdleTimeout` concerns idle keep-alive connections, not active requests.
Read-header timeout is independently capped at 10s even if read timeout is zero.
All three HTTP timeouts accept `0s`; heartbeat/backoff/job deadlines must be positive.

#### Polling protocol

1. Call `bash` with `{"workspaceId":"YOUR_ID","command":"git pull","timeout":3600}`.
2. Save `job_id` immediately. Call `bash_status` with
   `{"workspaceId":"YOUR_ID","job_id":"RETURNED_ID","offset":0,"maxBytes":65536}`.
3. Reuse `next_offset` as the next request's `offset`. Poll every 0.5–2s; drain more
   promptly when `has_more` is true. Do **not** resubmit a still-running command.
4. Finish only when state is `completed`, `failed`, `cancelled`, or `timed_out`
   **and** `has_more` is false. Inspect `exit_code` and `error`, not just `result`.
5. `bash_cancel` accepts `workspaceId` and `job_id`. Cancellation is asynchronous;
   poll for the final state. It also terminates ordinary descendants (and the full
   cgroup/Job Object tree when hard isolation is used).

`chunks` contains `{stream, offset, text, data_base64}`. `stream` is stdout/stderr;
`data_base64` preserves exact bytes even when binary/UTF-8 boundaries split across
chunks. The common byte cursor reflects read order, not a guaranteed ordering
between separately buffered stdout/stderr. `result` is a convenient text rendering.
Polling provides **incremental chunks**, intentionally without keeping an SSE stream
open for each command. Child programs may buffer their own output; use their
line-buffering/unbuffered options when needed.

Output is kept in a bounded in-memory tail, not `CombinedOutput` and not unbounded
files. Each job retains at most `outputBytes` raw bytes **and 512 chunks**; tiny
writes may hit the chunk cap first. `oldest_offset` and `dropped_bytes` explicitly
report overwritten output. Maximum polling payload is 128 KiB of raw output before
JSON/base64 overhead. Slow/offline clients can lose old output; redirect important
build logs to a workspace file when complete retention is needed.

Jobs are owned by the canonical workspace ID and survive HTTP/MCP reconnects,
**not a devspace process restart**. There is no unbounded queue: excess concurrency
or a full retained-job store yields an explicit tool error. Terminal jobs expire
after `retention`; active jobs have deadlines. Shutdown cancels and waits for jobs.
These controls bound bash output/jobs, not allocations made by unrelated tools.

#### Process priority and hard resource limits

- **Linux:** shell starts through `nice -n 10` and, when available, `ionice -c 3`
  (idle I/O class). A separate process group allows tree cancellation. Missing
  `ionice` is logged; disk-scheduler support for I/O priority varies.
- **Windows:** the child is created suspended with BELOW_NORMAL priority, assigned
  to a kill-on-close Job Object, configured, then resumed. This avoids an execution
  window before Job Object assignment. No breakaway permission is granted.
- **macOS/BSD:** `nice` and process-group cancellation are available; requesting
  hard resource limits fails explicitly rather than pretending to enforce them.

Hard limits default to **disabled** for compatibility with hosts lacking systemd,
privileges, or supported Windows policies. Enable them on the target host to obtain
CPU/memory quotas. With `enabled: true`, a setup failure produces a failed job;
there is **no automatic unbounded fallback**. `cpuPercent` means percent of all
logical CPUs, **per job**. Two jobs at 25% allow up to 50% total; choose aggregate
CPU/RAM limits that leave capacity for devspace, the tunnel, and the OS. Memory is
in MiB, per job including descendants. Concurrency/quotas are not a security sandbox.

**Linux hard-limit backend:** cgroup v2 + `systemd-run`, `systemctl`, a user systemd
manager with delegated CPU/memory controllers (`systemdUser: true`). Transient
services apply CPUQuota, MemoryMax, MemorySwapMax=0, TasksMax=256, IOWeight=10,
KillMode=control-group, and RuntimeMaxSec. An internal launcher verifies the actual
`cpu.max`/`memory.max` inside the service before running the command. A system
manager can be used with `systemdUser: false`; it launches as the devspace account's
UID/GID and requires explicitly provisioned permissions. Do not run the public MCP
server as root to work around permissions. Internal work uses no shell interpolation
for resource properties or launch arguments.

**Bandwidth:** `0` disables shaping. CPUQuota/IOWeight do **not** constrain network.
On Windows 10+/Server 2016+, Job Object network control limits **outbound** bytes/s
only; inbound clone traffic requires external network/QoS shaping. An unsupported
Job Object policy fails the job. Linux bandwidth uses an administrator-provisioned,
dedicated network namespace/veth and `tc` shaping in both directions, not an uplink
qdisc that would also throttle the tunnel:

```bash
# Isolated Linux test machine only; review routing, DNS and firewall first.
# Requires IPv4 forwarding already enabled. No firewall is disabled by this script.
sudo ./scripts/unix/setup-bash-network.sh devspace-bash ds-bash0 1048576
```

Set `enabled: true`, `systemdUser: false`, `bandwidthBytesPerSecond: 1048576`,
`networkNamespacePath: "/run/netns/devspace-bash"`, `networkInterface: "ds-bash0"`.
The helper prints setup/rollback guidance. Configure administrator-approved service
permissions for the unprivileged devspace account. The launcher checks the host
veth's TBF and ingress policing rates and rejects missing/looser policies; unusual
`tc` JSON formats also fail closed. Namespace membership/interface pairing and host
firewall permissions remain the administrator's responsibility. All jobs sharing
the namespace share its network cap. Ingress policing drops excess packets and TCP
backs off; this is rate limiting, not a promise of zero packet loss. The helper
uses 10.203.0.0/30; review overlaps and change it if necessary. Namespace jobs see a
different loopback network, so Git URLs using localhost may need adjustment.

#### Tunnel watchdog, stable URLs and limitations

HTTP binds **before** the tunnel starts. Tunnel output continues to drain after
URL detection (fixing the old full-pipe deadlock), with bounded log buffering and
exactly one `Wait` per process. A separate supervisor/watchdog handles exit,
startup timeout, failed health checks, restart and shutdown; it never shares bash
worker slots. Go has no per-goroutine priority API; no real-time scheduler setting
is applied to the server. CPU headroom comes from child priority and resource limits.

`tunnelHeartbeatInterval` sets SSH ServerAliveInterval and the watchdog's public
`/healthz` probe cadence, **not a cloudflared protocol flag**. SSH also uses
ServerAliveCountMax=3, TCPKeepAlive, ConnectTimeout and non-interactive mode.
Cloudflared's own QUIC/HTTP2 heartbeat/reconnect behavior remains its responsibility.
The watchdog restarts after three consecutive public probe failures (5s probe timeout)
or process exit. It handles prolonged initial unavailability too. Reconnect backoff
starts at `tunnelReconnectBackoff`, doubles to 60s with 50–100% jitter, resets after a
stable two-minute run, and is interruptible on shutdown. Quick-tunnel discovery
tries Cloudflare first, switching provider after two startup failures.

Zerolog events include timestamps, provider, cause, retry delay and failure count:
`tunnel_connected`, `tunnel_healthcheck_failed`, `tunnel_health_restored`,
`tunnel_disconnected_reconnecting`, `tunnel_stopped`. Bounded process-output tails
are debug-only; protect logs because public tunnel URLs are sensitive.

**A Quick Tunnel's URL can change after restart. An SSH key alone does not guarantee
a stable Pinggy URL.** The process can recover while a client still points to the
old address. For unattended operation, configure a named Cloudflare tunnel and its
credentials/ingress using cloudflared's normal configuration, then add:

```json
{
  "cloudflaredTunnelName": "YOUR-TUNNEL-NAME-OR-ID",
  "tunnelPublicUrl": "https://YOUR-STABLE-DOMAIN"
}
```

This runs `cloudflared tunnel --no-autoupdate run NAME`; configure its ingress to
the devspace local port. The watchdog probes that stable domain. Named tunnels do
not silently fall back to a random Pinggy URL. Health probes must be permitted by
any Access policy; redirects/login pages count as failures. This change does not
add MCP authentication: protect the public endpoint as described under Security.

No software can promise a tunnel **never** disconnects under host OOM, stalled
storage, OS/network failures or provider outages. Recovery cannot preserve an
already-broken TCP request, and the watchdog's public probe also depends on outbound
DNS/network health. Polling permits reconnects without losing an accepted job; hard
limits preserve capacity only when configured and enforced on the actual host.

#### Verification and acceptance

See [docs/VALIDATION.md](docs/VALIDATION.md) for the exact delivery validation status.
Go tests cover bounded output/cursors, workspace ownership, cancellation/deadlines,
MCP reconnects, process priority, cgroup checks, noisy tunnel pipes, hung-process
watchdog behavior and supervised restarts. Run on a machine with the Go version
required by `go.mod`:

```bash
./scripts/unix/verify-resilience.sh
```

The separate opt-in acceptance harness uses a >500 MiB incompressible Git repository,
a simultaneous >500 MiB HTTP transfer and CPU workers (a stress-ng equivalent):

```bash
# Load-generator host: create/serve an artificial fixture (needs several GiB).
python3 scripts/acceptance/prepare_fixture.py /tmp/devspace-load-fixture
# Follow the printed HTTP-server command, using a reachable test-network bind address.

# Driver: use a REAL public MCP tunnel and a disposable allowed workspace.
python3 scripts/acceptance/mcp_stress.py \
  --mcp-url https://YOUR-STABLE-DOMAIN/mcp \
  --repo-url http://LOAD_GENERATOR:8000/fixture.git \
  --download-url http://LOAD_GENERATOR:8000/source/payload.bin \
  --workers 4 --load-seconds 120 --deadline 3600 --max-latency 5 \
  --confirm-load --report acceptance-result.json
```

Set workers to the number of logical CPUs to offer full CPU load; an enabled CPU
quota will deliberately prevent actual host saturation. Use `--python python` on
Windows if appropriate. Thresholds, endpoint and duration must be chosen for the
deployment. Success requires zero observed health/MCP errors/timeouts, latency under
the threshold, a successful >500 MiB clone and concurrent transfer, and completion
through polling. Sampling cannot prove no sub-second disconnect occurred: correlate
with devspace/provider logs and run an appropriate soak test. The checkout is left
for inspection; clean it up manually. Test both providers separately, and separately
kill the tunnel process to verify backoff/recovery (an injected drop is NOT part of
the zero-drop load pass). A changed quick URL requires reconfiguring the client.



---

## Tunnel (Remote Access)

For ChatGPT web version (HTTPS required), Dev Space Go auto-starts a tunnel:

| Tunnel | URL type | Setup |
|---|---|---|
| **Cloudflare** | Random (auto) | `cloudflared.exe` included in `tools/` |
| **Pinggy** | Provider/plan dependent | SSH access; a key alone does not ensure a stable URL |

The server detects available tunnel executables and supervises reconnection. Quick Tunnel URLs can change after restart; use a named tunnel with a fixed domain for unattended recovery (see Configuration).

---

## Shell Support

| OS | Default | Alternatives |
|---|---|---|
| **Windows** | PowerShell | `cmd` / `pwsh` |
| **Linux** | bash | `sh` / any shell |
| **macOS** | bash | `sh` / `zsh` |

Set `"shell"` in config.json or choose in the GUI.

---

## Security

- **No built-in authentication** — anyone with the public tunnel URL can use the server
- **Path containment** — all file ops validated against allowed roots
- **Tunnel access** — treat the public tunnel URL as a secret and stop the server when it is not in use
- **No third-party uploads** — your code never leaves your machine

---

## Building from Source

```bash
git clone https://github.com/snakex21/devspace-go
cd devspace-go

# Build everything (all platforms)
.\scripts\windows\build.ps1     # Windows
./scripts/unix/build.sh          # Linux / Mac
make -f scripts/unix/Makefile    # Linux / Mac (make)

# Build just for current platform
go build -o devspace ./cmd/devspace/
go build -o devspace-gui ./cmd/devspace-gui/
```

---

## Platform Support

| Platform | Server | GUI |
|---|---|---|
| **Windows** | ✅ | ✅ |
| **Linux** | ✅ | 🔧 (compile natively) |
| **macOS Intel** | ✅ | 🔧 (compile natively) |
| **macOS M-chip** | ✅ | 🔧 (compile natively) |

GUI requires Fyne (OpenGL) — cannot cross-compile. Server compiles everywhere.

---

## Project Structure

```
devspace-go/
├── cmd/
│   ├── devspace/           ← CLI + MCP server
│   └── devspace-gui/       ← Desktop GUI configurator (Fyne)
├── internal/
│   ├── config/             ← Portable config system
│   ├── locales/            ← 47 language translations
│   ├── logger/             ← Structured logging (zerolog)
│   ├── server/             ← HTTP + MCP + tunnel orchestration
│   ├── store/              ← SQLite workspace sessions
│   ├── tools/              ← read, write, edit, grep, glob, ls, bash
│   └── workspace/          ← Workspace & path validation
├── scripts/
│   ├── windows/            ← PowerShell build script
│   ├── unix/               ← Bash + Makefile build scripts
│   └── userscripts/        ← Tampermonkey auto-approve script
├── readme/                 ← Translations of this file (47 languages)
├── tools/                  ← cloudflared.exe
├── go.mod / go.sum
└── README.md
```

---

Built in Go. Zero npm. Zero Node.js. One binary.

## Prepared precompiled release

See [publishing instructions](docs/PUBLICAR_GITHUB.md) and [release notes](docs/RELEASE_NOTES_v2.2.0.md).
The current prepared asset is Linux amd64 with glibc >= 2.34; other binaries must be built and tested separately.
