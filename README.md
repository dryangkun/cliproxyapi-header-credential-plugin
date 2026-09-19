# CLIProxyAPI Header Credential Router Plugin

A standalone CLIProxyAPI scheduler plugin that restricts each request to a credential pool supplied by an inbound HTTP header.

The plugin does **not** modify CLIProxyAPI.

## Typical architecture

Use Nginx to map a CPA API key to an allowed credential pool, then inject that pool into the upstream request:

```text
client
  -> Nginx
       CPA key -> credential pool
       X-CPA-Credentials: auth-a.json,auth-b.json,auth-c.json
  -> CLIProxyAPI
  -> this scheduler plugin
```

The plugin intersects the requested IDs with CLIProxyAPI's current scheduler candidates, so disabled, unavailable, model-incompatible, cooling-down, and retry-excluded credentials are not selected.

**Security:** do not trust a client-supplied `X-CPA-Credentials` value. Nginx should always overwrite or clear that header and set the value derived from the authenticated CPA key.

## Credential IDs

CLIProxyAPI derives a file-backed credential ID from the credential path relative to `auth-dir`.

For files directly under `auth-dir`, IDs typically look like:

```text
codex-xxx-xxx@gmail.com-pro.json
codex-xxx-xxx@gmail.com-plus.json
```

IDs are matched exactly and case-sensitively.

## Multiple credentials

The default header is:

```http
X-CPA-Credentials: codex-a@gmail.com-pro.json,codex-b@gmail.com-plus.json,codex-c@gmail.com-pro.json
```

Whitespace is ignored and duplicate IDs are removed.

The plugin computes:

```text
IDs allowed by X-CPA-Credentials
            ∩
current CLIProxyAPI scheduler Candidates
            =
current credential pool
```

If the pool contains credentials from multiple priority tiers, new selections use only the highest currently available priority tier.

An existing session binding is allowed to remain on a lower-priority credential while that credential is still available, matching CLIProxyAPI's session-affinity behavior.

## Routing strategies

The plugin supports:

- `round-robin` — default; tracks the last selected credential ID so temporary candidate removal does not reset rotation.
- `weighted-round-robin` — smooth weighted round robin using the candidate `weight` attribute. Default weight is 1; non-positive weights are excluded.
- `fill-first` — deterministically selects the first ID-sorted candidate until it becomes unavailable.

The strategy applies only inside the credential pool selected by `X-CPA-Credentials`.

## Session affinity

Session affinity is enabled by default.

The plugin currently recognizes explicit session headers available to the scheduler:

```text
X-Claude-Code-Session-Id
Session-Id
Session_id
X-Session-ID
X-Session-Affinity
X-Client-Request-Id
```

For Codex, `Session-Id` / `Session_id` is the main path.

A binding is scoped by:

```text
provider + model + credential pool + explicit session ID
```

Behavior:

1. First request for a session selects a credential from the allowed pool using the configured strategy.
2. Later requests reuse that credential while it is still present in the allowed pool and in CLIProxyAPI's current Candidates.
3. If the bound credential becomes unavailable or is excluded during retry, the plugin selects another currently eligible credential and updates the binding.
4. Session bindings expire after `session_affinity_ttl`.

Because the scheduler API exposes headers and metadata but not the original request body, this plugin does not currently reproduce CLIProxyAPI's body-derived affinity signals such as `prompt_cache_key`, conversation IDs, message-history hashes, or LCP matching. Explicit Codex/Claude/OpenCode/pi session headers are supported.

## Build

```bash
make build
```

Linux output:

```text
dist/linux/<arch>/header-credential-router.so
```

Direct build:

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

      header: "X-CPA-Credentials"

      strategy: "round-robin"

      session_affinity: true
      session_affinity_ttl: "1h"

      missing_behavior: "fallback"
      not_found_behavior: "reject"
```

Configuration:

| Field | Default | Description |
|---|---|---|
| `header` | `X-CPA-Credentials` | Header containing comma-separated credential IDs. |
| `strategy` | `round-robin` | `round-robin`, `weighted-round-robin`, or `fill-first`. |
| `session_affinity` | `true` | Pin explicit client sessions to one credential. |
| `session_affinity_ttl` | `1h` | Session binding lifetime. |
| `missing_behavior` | `fallback` | `fallback` or `reject` when the header is absent/empty. |
| `not_found_behavior` | `reject` | `fallback` or `reject` when none of the requested IDs are currently eligible. |

`enabled` and plugin `priority` are owned by CLIProxyAPI and ignored by this plugin's own config parser.

## Request example

```bash
curl https://your-cpa.example/v1/responses \
  -H 'Authorization: Bearer YOUR_CPA_KEY' \
  -H 'Content-Type: application/json' \
  -H 'X-CPA-Credentials: codex-a@gmail.com-pro.json,codex-b@gmail.com-plus.json' \
  -H 'Session-Id: codex-session-123' \
  -d '{"model":"gpt-5.6","input":"hello"}'
```

Normally the client-facing Nginx layer should inject `X-CPA-Credentials` rather than allowing the client to choose it.

## Nginx sketch

A simple pattern is:

```nginx
# $cpa_key should be derived from the authenticated request.
map $cpa_key $cpa_credentials {
    default "";
    key-a "codex-a.json,codex-b.json";
    key-b "codex-c.json,codex-d.json";
}

location / {
    # Always overwrite the client value.
    proxy_set_header X-CPA-Credentials $cpa_credentials;

    proxy_pass http://cliproxyapi;
}
```

For a production configuration, derive `$cpa_key` from the validated Authorization header or another trusted Nginx variable rather than directly trusting a client-controlled helper header.

## Failure behavior

With:

```yaml
not_found_behavior: reject
```

if none of the IDs allowed for the CPA key are currently present in CLIProxyAPI's Candidates, the request is rejected instead of escaping to an unrelated credential.

If you explicitly want unrestricted fallback:

```yaml
not_found_behavior: fallback
```

then CLIProxyAPI's normal selector is allowed to take over.

For strict key-to-pool isolation, keep `not_found_behavior: reject`.

## Tests

```bash
go test ./...
```

The project intentionally uses only the Go standard library and does not import CLIProxyAPI's Go packages.
