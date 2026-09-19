# CLIProxyAPI Header Credential Router Plugin

A standalone CLIProxyAPI scheduler plugin that pins one request to a credential selected by an inbound HTTP header.

The plugin does **not** modify CLIProxyAPI. It uses the native plugin ABI, the scheduler capability, and the `host.auth.list` host callback.

## Behavior

For each request:

1. Read the configured request header, by default `X-CPA-Credential`.
2. Receive the credentials currently eligible for the request from CLIProxyAPI.
3. Read credential metadata from `host.auth.list`.
4. Match the header value to the configured credential field, by default `account`.
5. Return the matching candidate `AuthID` to CLIProxyAPI.

The plugin opts into `scheduler_across_priorities`, so an explicitly selected credential can be found even when it is not in the highest priority tier.

Default behavior:

- Missing header: fall back to CLIProxyAPI's existing scheduler.
- Header present but credential unavailable/not eligible: reject the request.
- Credential metadata cache TTL: 5 seconds.

## Supported match fields

`match_field` supports:

- `account`
- `email`
- `label`
- `name`
- `auth_index`
- `id`
- `auto`

`auto` tries account, email, label, name, auth index, then ID.

If multiple eligible credentials match the same value, the plugin rejects the request as ambiguous instead of choosing arbitrarily.

## Build

```bash
make build
```

Linux output:

```text
dist/linux/<arch>/header-credential-router.so
```

You can also build directly:

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -o header-credential-router.so .
rm -f header-credential-router.h
```

## Install into CLIProxyAPI

CLIProxyAPI searches plugin libraries in:

```text
plugins/<GOOS>/<GOARCH>
plugins
```

Linux amd64 example:

```bash
mkdir -p /path/to/CLIProxyAPI/plugins/linux/amd64
cp dist/linux/amd64/header-credential-router.so \
  /path/to/CLIProxyAPI/plugins/linux/amd64/
```

The plugin ID is the library basename:

```text
header-credential-router
```

## CLIProxyAPI configuration

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    header-credential-router:
      enabled: true
      priority: 100
      header: "X-CPA-Credential"
      match_field: "account"
      case_sensitive: false
      missing_behavior: "fallback"
      not_found_behavior: "reject"
      cache_ttl_seconds: 5
```

Configuration:

| Field | Default | Description |
|---|---|---|
| `header` | `X-CPA-Credential` | Request header containing the credential selector. |
| `match_field` | `account` | Credential field compared with the header value. |
| `case_sensitive` | `false` | Whether matching is case-sensitive. |
| `missing_behavior` | `fallback` | `fallback` or `reject` when the header is absent/empty. |
| `not_found_behavior` | `reject` | `fallback` or `reject` when no eligible credential matches. |
| `cache_ttl_seconds` | `5` | Cache lifetime for `host.auth.list`; `0` disables caching. |

`enabled` and `priority` are owned by CLIProxyAPI and ignored by the plugin config parser.

## Request example

```bash
curl https://your-cpa.example/v1/responses \
  -H 'Authorization: Bearer YOUR_CPA_KEY' \
  -H 'Content-Type: application/json' \
  -H 'X-CPA-Credential: account-a' \
  -d '{"model":"gpt-5.6","input":"hello"}'
```

With the default config, this request can only select an eligible credential whose `account` equals `account-a`.

## Strict routing

With:

```yaml
not_found_behavior: reject
```

the plugin performs strict credential pinning. If the requested credential is disabled, cooling down, unsupported for the requested model, or excluded from the current scheduler candidates, another credential is not silently selected.

If failover is acceptable:

```yaml
not_found_behavior: fallback
```

CLIProxyAPI's normal scheduler takes over when the requested credential cannot be selected.

## Tests

```bash
go test ./...
```

The project intentionally uses only the Go standard library and does not import CLIProxyAPI's Go packages.
