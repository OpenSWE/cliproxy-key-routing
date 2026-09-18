# cliproxy-key-routing

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) scheduler plugin that gives each
inbound API key its own fallback order over the upstream credentials.

CLIProxyAPI pools every OAuth account and round-robins across them. Its `api-keys` list is
flat, so it cannot prefer one account for one client and another for the next. This plugin
reads the caller's key at pick time, looks it up in a sidecar YAML, and returns the first
account in that key's `order` that is currently live. Credentials in cooldown are already
removed by the host before the plugin runs, and a failed credential is excluded on retry, so
the order is a real fallback chain, not a filter.

- **Preference, not allowlist.** If no account named in the row is live, the plugin returns
  unhandled and the built-in scheduler serves from whatever remains.
- **Provider-agnostic.** Emails are matched against the auth file name of any provider.
- **Live reload.** The sidecar is re-read when its mtime changes. A broken edit keeps the last
  good table and logs the parse error.
- **Every pick is logged** through the host log with the key's last four characters, the
  chosen account and its rank, so the gateway log shows which account served each request.

## Install

Requires the Go toolchain matching the gateway's own Go version (`go version <binary>`), a C
compiler, and the same CLIProxyAPI module version as the running gateway in `go.mod`.

```sh
make build                          # dist/cliproxy-key-routing.dylib (or .so)
install -d ~/.cli-proxy-api/plugins
install -m 0644 dist/cliproxy-key-routing.dylib ~/.cli-proxy-api/plugins/
install -m 0600 host-routing.example.yaml ~/.cli-proxy-api/plugins/host-routing.yaml   # then edit
```

Gateway config:

```yaml
plugins:
  enabled: true
  dir: "~/.cli-proxy-api/plugins"     # default "plugins" is relative to the process cwd
  configs:
    cliproxy-key-routing:
      enabled: true
      priority: 1
      file: host-routing.yaml           # relative to the plugin's directory
```

Restart the gateway once so it loads the dylib. Later sidecar edits need no restart.

## Sidecar format

See `host-routing.example.yaml`. Each row is an inbound key and an ordered list of emails.
The key never appears in logs beyond its last four characters.

## Verify

```sh
grep 'key-routing' ~/.cli-proxy-api/logs/main.log | tail
```

Look for `key-routing: pick` lines with `account=` and `rank=0` for the primary. `rank=1` or
higher means the primary was cooling and the fallback served. `key-routing: pass-through` names
the reason: unbound key, no inbound key, or no named account live.

## Caveats

- The plugin host loads at most one scheduler plugin. This one is mutually exclusive with any
  other.
- The host refuses a plugin whose schema version is newer than its own, and a load failure is
  a warning that silently reverts to round-robin. Pin the gateway version and rebuild the plugin
  against the matching module when you upgrade.
- Session affinity, when enabled in the gateway, outranks the plugin for already-bound sessions.

## License

MIT
