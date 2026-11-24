# machine

Authenticated shell MCP server for Linux and macOS.

## Install

With Go 1.25 or later (automatic toolchain upgrades enabled):

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

## Security

The API token grants arbitrary shell execution as the server user. Use a dedicated unprivileged account and a secure SSH tunnel for remote access. Limits are not a sandbox. Never retry a command automatically after an ambiguous network failure.

MIT license.
