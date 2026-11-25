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
See machine.example.yaml for optional settings. Only the shell tool is exposed. It accepts command and optional absolute workdir.

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
