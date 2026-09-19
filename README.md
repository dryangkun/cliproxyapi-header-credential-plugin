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

Because the scheduler API exposes headers and metadata but not the original request body, this plugin does not currently reproduce CLIProxyAPI's body-derived affinity signals such as `prompt_cache_key`, conversation IDs, message-history hashes, or LCP matching. Explicit Codex/Claude/OpenCode/pi session headers are supported.\n\nSession bindings are stored in plugin process memory. They are reset when CLIProxyAPI/plugin state is restarted or reconfigured, and they are not shared across multiple CLIProxyAPI replicas.

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

## Scheduler precedence in CLIProxyAPI

No extra CPA setting is required to make this plugin run before the built-in credential selector.

In normal local credential mode, CLIProxyAPI's selection flow is:

```text
eligible credentials
        ↓
Plugin Scheduler
        ↓
Handled=true
   ├─ yes → use plugin-selected AuthID
   └─ no  → fall back to CPA built-in selector
```

This means that once `header-credential-router` is loaded and registered as a Scheduler plugin, it is consulted before CPA's built-in `round-robin`, `weighted-round-robin`, `fill-first`, or session-affinity selector.

The plugin only yields back to the built-in selector when it returns `Handled=false`. In this plugin that can happen, for example, when:

- `X-CPA-Credentials` is missing and `missing_behavior: fallback`;
- no requested credential is currently eligible and `not_found_behavior: fallback`.

For strict key-to-credential-pool isolation, use:

```yaml
missing_behavior: fallback
not_found_behavior: reject
```

With this configuration, ordinary requests without the Nginx routing header can still use CPA's normal selector, while requests that do contain a credential pool cannot escape that pool when all listed credentials are unavailable.

### Multiple Scheduler plugins

CLIProxyAPI does not chain all Scheduler plugins. It selects the first active Scheduler plugin from the plugin records sorted by:

1. higher `priority` first;
2. plugin ID as a deterministic tie-breaker.

Therefore, if other Scheduler plugins are installed, configure this plugin with a higher priority, for example:

```yaml
plugins:
  configs:
    header-credential-router:
      enabled: true
      priority: 1000
```

If this is the only Scheduler plugin, the exact priority value does not matter.

### Home dispatch mode caveat

CLIProxyAPI's Home credential-dispatch path is checked before the local/plugin scheduler path. If the auth manager is actually running with Home dispatch enabled, credential selection goes through Home and this Scheduler plugin is bypassed.

Using a management UI or plugin-store registry by itself is not the important distinction; the relevant condition is whether CPA is using Home for credential dispatch.

## CLIProxyAPI host configuration

CLIProxyAPI disables dynamic-library plugins by default, so the host must explicitly enable them.

A minimal CPA configuration is:

```yaml
# Required only if you want the plugin's debug-level host.log messages.
# The plugin works normally with debug: false.
debug: true

plugins:
  # Required: globally enable dynamic plugins.
  enabled: true

  # Required unless you use another path.
  # Relative paths are resolved by CLIProxyAPI using its normal config/runtime path rules.
  dir: "plugins"

  configs:
    # Must match the dynamic-library basename:
    # header-credential-router.so -> header-credential-router
    header-credential-router:
      # Required: per-plugin enable flag.
      enabled: true

      # Recommended: scheduler-plugin priority.
      # If several scheduler plugins are installed, higher-priority plugins are considered first.
      priority: 100

      header: "X-CPA-Credentials"
      strategy: "round-robin"
      session_affinity: true
      session_affinity_ttl: "1h"
      log_level: "debug"
      missing_behavior: "fallback"
      not_found_behavior: "reject"

# Optional: write CPA logs to rotating log files instead of stdout.
# logging-to-file: true
```

Important distinctions:

- `plugins.enabled: true` is required. Enabling only `plugins.configs.header-credential-router.enabled` is not enough.
- `debug: true` is **not required for routing**. It is only needed if you want CPA to emit the plugin's `debug`-level `host.log` records. With `log_level: info`, CPA can keep `debug: false`.
- `logging-to-file: true` is optional. Without it, logs go to the normal CPA stdout/stderr destination.
- `store-sources` is optional and is only for plugin-store/Home installation and updates. It is not required to load or run an already installed `.so/.dylib/.dll`.
- No extra CPA switch is required for the plugin's request-interceptor capability. Once this plugin is loaded and registered, CPA invokes it automatically.
- If multiple scheduler plugins are enabled, make sure this plugin's `priority` is high enough for the desired ordering.

### Plugin file placement

For Linux amd64, for example:

```text
<plugins dir>/linux/amd64/header-credential-router.so
```

CLIProxyAPI also supports loading from the root plugin directory, but the OS/architecture subdirectory is preferred for multi-platform layouts.

The plugin ID is derived from the dynamic-library basename:

```text
header-credential-router.so
        ↓
header-credential-router
```

Therefore the key under `plugins.configs` must be exactly:

```yaml
header-credential-router:
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

      log_level: "info"

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
| `log_level` | `info` | `off`, `info`, or `debug`. Use `debug` to inspect requested/candidate credential IDs and header stripping. |
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

## Routing logs

The plugin writes routing diagnostics through CLIProxyAPI's native `host.log` callback, so the messages appear in the normal CPA logs.

At `info` level it records important decisions such as:

- selected credential ID and strategy;
- session-affinity reuse;
- session binding/rebinding;
- requested pool with no currently eligible credential.

Set:

```yaml
log_level: "debug"
```

to additionally log the requested credential IDs, CPA candidate IDs, and confirmation that the routing header was removed before the upstream request.

## Routing header is never forwarded upstream

`X-CPA-Credentials` is needed during scheduler selection, so it cannot be removed by Nginx before CPA receives the request.

This plugin also registers CLIProxyAPI's request-interceptor capability. The flow is:

```text
scheduler reads X-CPA-Credentials
        ↓
credential selected
        ↓
request.intercept_after
        ↓
ClearHeaders: [X-CPA-Credentials]
        ↓
upstream request
```

Therefore the configured routing header is removed after credential selection and before the request is sent to the provider. The behavior follows the configured `header` value, so changing the header name also changes which header is stripped.


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
