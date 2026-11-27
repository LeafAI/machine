# machine

Authenticated shell MCP server for Linux and macOS.

## Install

With Go 1.27 or later (automatic toolchain upgrades enabled):

```sh
go install github.com/LeafAI/machine@latest
```

Ensure Go's binary directory is on PATH. Run `machine --help` to check installation.

## Configure

```sh
mkdir -p "$HOME/.config"
(umask 077; printf 'api_key: "%s"\n' "$(openssl rand -hex 32)" > "$HOME/.config/machine.yaml")
chmod 600 "$HOME/.config/machine.yaml"
machine
```

Do not overwrite an existing configuration unless you intend to rotate its token.
Connect a Streamable HTTP MCP client to http://127.0.0.1:6666/mcp with Authorization: Bearer YOUR_TOKEN.
See machine.example.yaml for optional settings. By default only the shell tool is exposed. It accepts command and optional absolute workdir.

## Disable authentication (trusted local use only)

Set the following in ~/.config/machine.yaml:

```yaml
listen: "127.0.0.1:6666"
auth_required: false
```

The api_key field may be omitted. Restart machine and remove the Authorization header from your MCP client configuration. Authentication defaults to enabled if auth_required is omitted. All local HTTP requests must originate from loopback and use a loopback Host header; browser Origin headers are rejected in both authentication modes. Keep file permissions at 0600.

Any local process can execute arbitrary commands as the server user in this mode. Do not forward or proxy the unauthenticated endpoint, including through SSH tunnels.

## Security

The API token grants arbitrary shell execution as the server user. Use a dedicated unprivileged account and a secure SSH tunnel for remote access. Limits are not a sandbox. Never retry a command automatically after an ambiguous network failure.

MIT license.

## OpenAI Secure MCP Tunnel

Optional, disabled by default. Uses the official embedded Go SDK (v0.0.21) with an in-memory MCP transport, not a public listener. The existing HTTP listener and its authentication settings are unchanged. Tunnel access is independently governed by OpenAI tunnel access controls, not the local API key.

1. Provision a tunnel at https://platform.openai.com/settings/organization/tunnels and obtain a runtime API key with Tunnels Read + Use permissions (not an admin key).
2. Add to machine.yaml:

```yaml
tunnel:
  enabled: true
  id: tunnel_REPLACE_WITH_REAL_ID
  api_key: "sk-REPLACE_WITH_RUNTIME_KEY"
```

3. Run machine. The runtime key can be stored in tunnel.api_key or supplied via CONTROL_PLANE_API_KEY (a nonempty environment value takes precedence). Keep machine.yaml at mode 0600 and never commit real credentials. The ID may instead be supplied via CONTROL_PLANE_TUNNEL_ID. Optional organization_id or CONTROL_PLANE_ORGANIZATION_ID selects the organization. Wait for the tunnel ready log.
4. In ChatGPT's app/connector settings, configure the provisioned Secure MCP Tunnel if supported by your account. See https://github.com/openai/tunnel-client/blob/v0.0.21/docs/onboarding.md.

Tunnel access grants arbitrary shell execution under the machine user's OS permissions. Restrict tunnel access to trusted principals, use a dedicated unprivileged account, and do not store unrelated secrets in that account. Removing runtime credentials from subprocess environments is defense in depth only, not isolation. Stop machine to disconnect. If the runtime stops unexpectedly or fails readiness within two minutes, machine shuts down.

## Dynamic shell configuration

Before each shell invocation, machine reads and validates ~/.config/machine.yaml. Changes to additional_paths, shell, timeout_seconds and max_output_bytes apply to the next invocation, including tunneled calls. Running commands retain their original settings. Listener, authentication, max_concurrent and tunnel settings require a restart. The entire YAML file must remain valid, owned by the server user, a regular non-symlink file, private (mode 0600), at most 1 MiB, and contain exactly one YAML document; if reading or validation fails, the command is not executed and a structured error is returned. Correct the file and retry without restarting. Prefer saving via atomic file replacement with private permissions.

## Shell results and limits

A command failure, timeout, invalid execution configuration or saturated concurrency limit is returned as structured output with exit_code and error, rather than an MCP tool exception. Output limits apply separately to stdout and stderr. Commands run with the server user's permissions; process-group cancellation is best effort and does not contain detached processes. Use an isolated OS account or container if isolation is required. Changes to server startup settings require restarting machine.

## Native agent tools (opt-in)

Add these settings to machine.yaml and restart once to register the tools. Choose only the tools you need and replace the workspace paths with existing trusted directories.

```yaml
tools:
  enabled: [shell, file_read, file_write, file_edit, file_info, file_search, file_grep, file_glob, directory_list, batch]
filesystem:
  default_workdir: ~/workspace
  allowed_roots: [~/workspace, /tmp]
  max_read_bytes: 1048576
  max_write_bytes: 4194304
  max_search_results: 200
  respect_gitignore: true
  follow_symlinks: false
batch:
  max_operations: 16
  max_parallel: 4
  max_output_bytes: 262144
```

All native operations return a structured result and optional error. Native tools require explicit allowed roots and use Go os.Root to confine filesystem access. Observed symlinks are rejected; unrestricted shell access, same-user processes, hard links and filesystem mounts are not sandboxed. Filesystem settings and limits are read per invocation. Tool registration and the global concurrency cap require restart. A relative path is resolved against default_workdir. Reads reject oversized or binary files rather than silently truncating them. Writes default to no overwrite, with atomic replacement and optional expected_sha256. Hash checks serialize native writes but cannot guarantee compare-and-swap against external writers. Search uses Go regular expressions, bounded traversal, and a subset of nested gitignore rules (not complete Git semantics). Batch accepts only enabled read-only tools, not shell, writes, edits or nested batches. Results are returned in request order; the output budget may omit trailing results.

Shell now accepts timeout_seconds, max_output_bytes and output_mode (head, tail, head_tail). Limits cannot exceed configured maximums. Output includes duration_ms and optional signal. A head_tail response concatenates the retained beginning and end; truncated indicates omitted bytes.

## Tool call logging

Compact start and completion lines include tool name, call ID, elapsed time and status; shell includes exit status, output sizes, timeout and truncation. Batch operations have individual lines. All logs go to stderr. The default tunnel log level is warn; use info or debug for troubleshooting. Parameters are opt-in because commands, paths and search patterns may contain secrets. File bodies and edit text are always redacted, and argument output is capped. Logs do not include tool output or raw error messages. Settings require restart.

```yaml
logging:
  level: info
  tunnel_level: warn
  log_arguments: true
  max_argument_bytes: 512
```

### Compact log layout

Tool calls use local time `YY-MM HH:MM:SS`, single-letter levels `D/I/W/E`, minimum four-digit sequence numbers and `<` / `>` for incoming request / outgoing result. With `log_arguments: true`, tool-specific key/value summaries show paths, commands, ranges and search patterns; file bodies and replacement strings are represented by byte counts. Free-form arguments may still contain secrets. Example:

```text
26-11 14:20:01 I 0001 file_read < path="main.go" lines=1-80
26-11 14:20:01 I 0001 file_read > ok 3ms
```

### Forward-compatible configuration

Unknown YAML keys, including nested settings from newer versions, are ignored. At startup machine emits a warning if unknown keys are present, without logging configuration values. Known settings still require valid types and values; malformed YAML, invalid authentication or resource limits, unsafe file permissions and multiple YAML documents remain rejected. During operation, invalid configuration rejects tool calls without stopping the running server. Unknown keys are not an indication that a feature is supported: verify the installed version before relying on a new security setting. Keep configuration updates atomic.
### Input and output previews

To show parameters and results, enable both options explicitly and restart machine:

```yaml
logging:
  level: info
  tunnel_level: warn
  log_arguments: true
  max_argument_bytes: 512
  log_output: true
  max_output_bytes: 1024
```

Both argument and output previews are disabled by default. The startup log reports the effective settings. Output previews contain actual tool result data, including shell stdout/stderr, file contents and errors; they can expose secrets. Enable only in a trusted environment and protect stderr logs. The output limit is independent of tool execution limits and does not alter MCP responses. Large previews are marked truncated; batch operations and aggregate batch results are logged separately.

### Tunnel recovery and long-running commands

An unexpected tunnel runtime shutdown or in-memory MCP session closure no longer terminates machine's local HTTP server. The tunnel supervisor creates a fresh SDK client and MCP session after a retry delay starting at one second and capped at 30 seconds. It logs the disconnection reason and retry delay, and stops retrying promptly on process shutdown. A lost request may be cancelled; machine never automatically replays shell commands or other tool calls. The control plane can enforce its own response deadline independently of machine's shell timeout. For long-running work, use short invocations to start a job and inspect progress/result separately, taking care not to repeat side effects. A longer shell timeout does not extend the control-plane response deadline.

## Optional outbound proxy

```yaml
proxy:
  enabled: true
  url: socks5://127.0.0.1:7891
  no_proxy: [localhost, 127.0.0.1, '::1']
  tunnel: true
  shell: true
```

Supported schemes are http, https, socks4, socks4a, socks5 and socks5h. Specify a port. HTTP(S) and SOCKS5 credentials can be embedded in the URL (percent-encode special characters); SOCKS4 supports only a user ID. Environment references such as `${MACHINE_PROXY_URL}` are expanded. Proxy configuration is disabled by default and preserves inherited proxy behavior when disabled or when the corresponding scope is false. Tunnel routing is configured once at startup, via the default HTTP transport cloned by the embedded SDK; restart to change it. Shell proxy settings apply to the next call via standard uppercase and lowercase proxy environment variables. Shell programs may ignore these variables; this is not a network sandbox. SOCKS4/4a tunnel connections use a native handshake, and shell support depends on the application. Loopback destinations bypass configured proxy routing. No direct fallback is attempted on a proxy connection failure for proxied HTTP requests. Keep credentials out of tool arguments and output logs. The tunnel SDK's own proxy detection/telemetry still uses inherited environment variables and may not recognize the configured transport route. Do not configure the SDK's integration Unix socket override together with a YAML tunnel proxy.

### Terminal colors

Set `logging.color: auto` (default), `always`, or `never`. Auto detects stderr TTYs on macOS and Linux and respects nonempty `NO_COLOR` and `TERM=dumb`. Standard ANSI colors work with Kitty and other compatible terminals. Incoming requests use `<` (cyan), outgoing results use `>` (magenta), sequence numbers are blue, and severity is color-coded. Tool data is sanitized for terminal control characters. This setting controls machine's own logs, not the tunnel SDK's internal logging. Restart to apply.

### Tunnel request correlation

The in-memory tunnel transport uses a single physical reader per connection generation, unique downstream RPC aliases, and a bounded pending map keyed by upstream request, shard and session identifiers. The original JSON-RPC ID is restored only when returning the response to the matching SDK worker. Missing, duplicate, cancelled and unmatched requests fail closed; stale replies are discarded. Tool responses include a machine-generated `tool_call_id` matching the compact start/completion logs. Debug logs associate tunnel request, session, connection generation, RPC alias and tool call identifiers. The upstream SDK still controls session authentication, delivery and deadlines. This layer does not provide tenant isolation, and does not extend the control-plane deadline or preserve a physical connection the SDK closes. Unowned server-initiated messages are not forwarded; machine tools do not currently use progress notifications or server-initiated requests. Async shell jobs are a separate feature.

### Tunnel polling and reconnect timing

The embedded SDK marks a tunnel ready after its first successful control-plane poll. With no queued commands, that poll can wait for the configured long-poll timeout (30 seconds by default). The tunnel can receive work before readiness is logged. To shorten idle readiness confirmation, configure:

```yaml
tunnel:
  enabled: true
  poll_timeout_seconds: 5
  poll_deadline_guardrail_seconds: 5
  reconnect:
    initial_delay_seconds: 1
    max_delay_seconds: 30
```

Defaults are 30, 5, 1 and 30 seconds respectively; zero selects the default. Accepted ranges are poll 1–120, guardrail 1–60, initial delay 1–60 and max delay initial–300 seconds. All settings are startup-only. Shorter polling increases idle control-plane requests and may affect SDK HTTP deadlines for response delivery and metadata as well; the guardrail is not a tool execution timeout. The embedded SDK does not expose a separate first-poll setting. The supervisor logs the attempt number, SDK startup duration, time spent awaiting first successful poll and total attempt readiness time; SDK cleanup duration is available at debug level. Retry delay is separate from those timings. This does not extend the upstream tool request deadline.

### macOS desktop notifications (opt-in)

Add `notify` to `tools.enabled` to enable the eleventh tool. No filesystem roots are required for `notify` alone. Restart the server to register the tool. Defaults and limits under `notify` are reloaded for each invocation.

```yaml
notify:
  default_title: Machine
  default_mode: dialog
  default_timeout_seconds: 0
  default_sound: ""
  max_message_bytes: 4096
  max_pending_dialogs: 4
```

Example arguments: `{"message":"Task done","title":"Notice","mode":"dialog"}`. Use `mode: notification` for a Notification Center banner, optionally with `sound: Glass`. Dialogs have an OK button and optional `timeout_seconds` (0 means wait indefinitely); sounds apply only to notifications. An explicit empty sound disables the configured default. `notify` is unsupported on Linux and cannot be invoked through read-only `batch`. The response follows the native tool envelope: `{"tool_call_id":"call_...","result":{"status":"submitted","mode":"dialog"}}`. Submission confirms only that `/usr/bin/osascript` started, not that a popup was displayed or acknowledged. A graphical login session and appropriate macOS permissions/settings are required. A LaunchDaemon or headless SSH environment may not display UI. Notifications may contain sensitive text visible on screen and in process arguments. Message and title are excluded from argument logs. A fixed AppleScript receives literal argv values; no shell evaluation. Child processes are reaped asynchronously and share a bounded pending-process limit per server (1–32); this does not rate-limit completed notifications. Notifications have a 30-second process watchdog; timed dialogs have an additional 15-second watchdog. Untimed dialogs can remain open until acknowledged, including across tunnel reconnections. They are not durable jobs; process restart does not guarantee cleanup or delivery. Notification process failures after submission are logged without message content.
