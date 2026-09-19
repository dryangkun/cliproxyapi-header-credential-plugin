# CLIProxyAPI Header Credential Router Plugin

A standalone CLIProxyAPI scheduler plugin that selects one credential by **exact credential ID** from an inbound HTTP header.

The plugin does **not** modify CLIProxyAPI.

## Credential ID

CLIProxyAPI derives a file-backed credential ID from the credential path relative to `auth-dir`.

If the credential files are directly under `auth-dir`, IDs look like:

```text
codex-xxx-xxx@gmail.com-pro.json
codex-xxx-xxx@gmail.com-plus.json
```

The request header value must exactly equal one of these IDs.

There is no matching by account, email, label, substring, or fuzzy comparison.

## Behavior

For each request:

1. Read the configured request header, default `X-CPA-Credential`.
2. Read the credentials currently available in `SchedulerPickRequest.Candidates`.
3. Compare the header value directly with `Candidates[].ID`.
4. If exactly one candidate ID matches, return that ID to CLIProxyAPI.

The plugin opts into `scheduler_across_priorities`, so an explicitly requested credential can be considered even when it is not in the highest priority tier.

Default behavior:

- Missing header: fall back to CLIProxyAPI's existing scheduler.
- Header present but the ID is not currently eligible: reject the request.
- ID matching is exact and case-sensitive.

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

The plugin ID is:

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
      missing_behavior: "fallback"
      not_found_behavior: "reject"
```

Configuration:

| Field | Default | Description |
|---|---|---|
| `header` | `X-CPA-Credential` | Request header containing the exact credential ID. |
| `missing_behavior` | `fallback` | `fallback` or `reject` when the header is absent/empty. |
| `not_found_behavior` | `reject` | `fallback` or `reject` when no eligible candidate has that ID. |

`enabled` and `priority` are owned by CLIProxyAPI and ignored by the plugin config parser.

## Request example

Select the Pro credential:

```bash
curl https://your-cpa.example/v1/responses \
  -H 'Authorization: Bearer YOUR_CPA_KEY' \
  -H 'Content-Type: application/json' \
  -H 'X-CPA-Credential: codex-xxx-xxx@gmail.com-pro.json' \
  -d '{"model":"gpt-5.6","input":"hello"}'
```

Select the Plus credential:

```bash
curl https://your-cpa.example/v1/responses \
  -H 'Authorization: Bearer YOUR_CPA_KEY' \
  -H 'Content-Type: application/json' \
  -H 'X-CPA-Credential: codex-xxx-xxx@gmail.com-plus.json' \
  -d '{"model":"gpt-5.6","input":"hello"}'
```

## Strict routing

With:

```yaml
not_found_behavior: reject
```

the plugin performs strict credential pinning. If the requested ID is disabled, cooling down, unsupported for the requested model, or otherwise absent from the current candidate list, the request is rejected instead of silently selecting another credential.

If failover is acceptable:

```yaml
not_found_behavior: fallback
```

CLIProxyAPI's normal scheduler takes over when the requested ID cannot be selected.

## Tests

```bash
go test ./...
```

The project intentionally uses only the Go standard library and does not import CLIProxyAPI's Go packages.
