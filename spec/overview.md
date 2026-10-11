# Overview

The central reference: provider configuration, transports, resource inventory, and conventions
that apply everywhere. Terms are defined in [`../GLOSSARY.md`](../GLOSSARY.md), and the reasons
behind them are in [`../adr/`](../adr/).

## Provider configuration

```hcl
terraform {
  required_providers {
    homeassistant = {
      source = "toelke/homeassistant"
    }
  }
}

provider "homeassistant" {
  url   = "http://homeassistant.local:8123"
  token = var.ha_token
}
```

| Argument | Type | Env fallback | Notes |
|---|---|---|---|
| `url` | string | `HOMEASSISTANT_URL` | Required (via argument or env). Base URL without `/api` |
| `token` | string, sensitive | `HOMEASSISTANT_TOKEN` | Required. Long-lived access token of an admin user |
| `insecure` | bool | `HOMEASSISTANT_INSECURE` | Skip TLS verification. Default `false` |
| `timeout` | string (duration) | `HOMEASSISTANT_TIMEOUT` | Per-request timeout. Default `"30s"` |

Supported clients: OpenTofu or Terraform ≥ 1.8 (ADR-0016). Supported HA: the six most recent
monthly releases (ADR-0003).

## Transports (ADR-0004)

One `HAClient` holds:

- **REST client** — `Authorization: Bearer <token>`. It returns typed errors: `ErrNotFound`
  (404), `ErrUnauthorized` (401), and `ErrUnreachable` (connection failure). Any other non-2xx
  status is an `*HTTPError` carrying the status code and the start of the body.
- **WebSocket client** — dialled lazily on the first command to `/api/websocket`. It performs
  the auth handshake (`auth_required` → `auth` → `auth_ok`), then multiplexes commands by an
  incrementing `id`. A dropped connection fails all pending commands with `ErrConnectionLost`
  (retryable), and the next command dials again. `auth_invalid` returns `ErrUnauthorized`, a failed
  dial `ErrUnreachable`, and a `success: false` result a `*WSError` carrying `code` and `message`.
  The provider `timeout` bounds each command, including a dial. A dial that times out is
  `ErrUnreachable`. A command that times out drops the connection, so a half-open socket is not
  reused and the next command dials again.

Which operation uses which transport:

| Concern | Transport | Endpoints / commands |
|---|---|---|
| Entity states, single state | REST | `GET /api/states`, `GET /api/states/<entity_id>` |
| Templates | REST | `POST /api/template` |
| Instance config | REST | `GET /api/config` |
| Automation / script / scene config | REST | `GET/POST/DELETE /api/config/<domain>/config/<id>` |
| Config flows (create, reconfigure) | REST | `POST /api/config/config_entries/flow`, `POST/DELETE …/flow/<flow_id>` |
| Delete config entry | REST | `DELETE /api/config/config_entries/entry/<entry_id>` |
| Read config entries | WS | `config_entries/get` |
| Floor / area / label registry | WS | `config/<x>_registry/list|create|update|delete` |
| Entity / device registry | WS | `config/entity_registry/get|update`, `config/device_registry/list|update` |
| Input helpers | WS | `<domain>/list|create|update|delete` (id field `<domain>_id`) |
| Dashboards | WS | `lovelace/dashboards/list|create|update|delete`, `lovelace/config`, `lovelace/config/save|delete` |

## Inventory

### Data sources

| Name | Spec |
|---|---|
| `homeassistant_config` | [data-sources.md](data-sources.md) |
| `homeassistant_entity` | [data-sources.md](data-sources.md) |
| `homeassistant_entities` | [data-sources.md](data-sources.md) |
| `homeassistant_template` | [data-sources.md](data-sources.md) |
| `homeassistant_area`, `homeassistant_areas` | [registries.md](registries.md) |
| `homeassistant_device`, `homeassistant_devices` | [registries.md](registries.md) |
| `homeassistant_integration` | [integrations.md](integrations.md) |

### Resources

| Name | ID kind | Spec |
|---|---|---|
| `homeassistant_floor` | generated | [registries.md](registries.md) |
| `homeassistant_area` | generated | [registries.md](registries.md) |
| `homeassistant_label` | generated | [registries.md](registries.md) |
| `homeassistant_entity_settings` | `entity_id` | [registries.md](registries.md) |
| `homeassistant_device_settings` | `device_id` | [registries.md](registries.md) |
| `homeassistant_automation` | user-chosen | [automations.md](automations.md) |
| `homeassistant_script` | user-chosen | [automations.md](automations.md) |
| `homeassistant_scene` | user-chosen | [automations.md](automations.md) |
| `homeassistant_input_boolean`, `_number`, `_text`, `_select`, `_datetime`, `_button` | generated | [helpers.md](helpers.md) |
| `homeassistant_dashboard` | `url_path` | [dashboards.md](dashboards.md) |
| `homeassistant_default_dashboard` | singleton (`default`) | [dashboards.md](dashboards.md) |
| `homeassistant_integration` | `entry_id` | [integrations.md](integrations.md) |
| `homeassistant_esphome`, `homeassistant_mqtt` | `entry_id` | [integrations.md](integrations.md) |

### Functions (ADR-0007)

Builder functions under `provider::homeassistant::*`, returning plain objects for dynamic config.
See [functions.md](functions.md).

## Conventions

**IDs.** Each resource's import ID is its `id` attribute. Schema descriptions state the format.
Generated IDs follow ADR-0009 (collision check, optional `id` via create-then-rename).
User-chosen IDs follow ADR-0008 (`^[a-z0-9_]+$`, change forces replacement); create fails if
the ID is taken (ADR-0024).

**Dynamic config.** Attributes named `config` are dynamic, use the semantic-equality custom type
(ADR-0006), and are passed to HA unchanged (ADR-0005). HA may rewrite a config when it saves it,
for example renaming old automation keys. So after each create and update, the provider records a
hash of what HA stored (the **stored baseline**, in private state), and on refresh a read-back
equal to the baseline is never drift (ADR-0023).

**Errors.**

- An unreachable HA is an error, with a hint to use `-refresh=false` (ADR-0011).
- 401 → "token invalid or expired".
- WS `success: false` → surface `error.code` and `error.message`.
- HA rejecting a `config` (REST 400, or a WS error when saving it) → a diagnostic on the `config`
  attribute.
- Config flow `abort` → surface the reason, with friendly text for `already_configured` and
  `single_instance_allowed` (suggest import), `cannot_connect`, and `invalid_auth`. The last two
  get the same text when a form rejects its data with them.
- A generated-ID create or rename that fails suggests a name clash (import, rename, or another
  name) only when HA rejected it, not when HA is unreachable or the call timed out.
- A compensating delete, which undoes a half-done create, runs even if the context was cancelled
  (Ctrl-C). The client's own timeout still bounds it.
- 404 / missing on Read → `RemoveResource` (the object was deleted outside Tofu).

**Eventual consistency.** HA reloads automations, scripts, and scenes asynchronously after a
write, and integrations create entities asynchronously. Operations that depend on such a result
poll it with backoff, up to the resource's `timeouts` (default 60s).

**Runtime state** is never managed (ADR-0012).

## Code layout (Go)

```
main.go
internal/
  provider/         provider schema, Configure, registration of resources/data sources/functions
  client/           HAClient; rest.go, websocket.go, configflow.go, plus one file per HA concept
  dyntype/          dynamic-config custom type + normalisation (ADR-0006)
  resources/        one file per resource family
  datasources/
  functions/        builder functions
  acctest/          testcontainers HA + headless onboarding helpers
examples/           tfplugindocs examples (one per resource/data source/function)
templates/          tfplugindocs templates where the defaults are insufficient
docs/               generated — never edited by hand
```

The client is consumed through interfaces, so resources and the config-flow driver are
unit-testable against fakes.
