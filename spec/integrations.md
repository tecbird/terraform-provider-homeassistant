# Integrations (config entries)

Config flows are REST. Reading config entries is WebSocket. A config entry's input data cannot be
read back (ADR-0014).

## Config-flow driver

```
ConfigFlows.Run(ctx, handler string, entryID string /* "" or reconfigure target */, steps []FlowStep) (entryID, error)

POST /api/config/config_entries/flow            {"handler": h, "show_advanced_options": true[, "entry_id": id]}
loop on result.type:
  "form"         → if result.errors is non-empty → abort + error with the errors
                   find the step with result.step_id (else abort + error);
                   a step already submitted once → abort + error (no loops)
                   POST /api/config/config_entries/flow/<flow_id>   step.data ∪ step.sensitive_data
                   (on a HTTP error, e.g. 400 for data not matching the schema → abort + error)
  "create_entry" → return result.result.entry_id
  "abort"        → reconfigure (entryID set) with reason "reconfigure_successful" → return entryID;
                   otherwise error with result.reason (HA already ended the flow, no DELETE)
  other          → abort + error (unsupported: external steps, progress, menus)
```

"abort" in the right-hand column means `DELETE /api/config/config_entries/flow/<flow_id>`. It is
sent even if the context was cancelled. Steps are matched by `step_id`, not by position. On a key
present in both maps, `sensitive_data` wins.

The loop only asks the caller for the data of each form. `homeassistant_integration` answers by
`step_id` as above; the typed integrations answer from field values (see *Answering forms from
fields*), which may answer the same `step_id` more than once.

Unit-tested against a fake HTTP server, with no real HA.

## `homeassistant_integration` (generic)

```hcl
resource "homeassistant_integration" "shelly" {
  domain = "shelly"
  steps = {
    user = { host = "192.168.1.50" }
  }
}

resource "homeassistant_integration" "cloud" {
  domain = "my_cloud"
  steps = {
    user = { username = "me@example.com" }
  }
  sensitive_steps = {
    user = { password = var.cloud_password }
  }
}
```

| Argument | Type | |
|---|---|---|
| `domain` | string | required, forces replacement |
| `steps` | object: `step_id` → object of fields (dynamic) | required, at least one step; forces replacement |
| `sensitive_steps` | same shape as `steps`, sensitive | optional; forces replacement |

The steps are two dynamic objects keyed by `step_id` (ADR-0021, which supersedes ADR-0014's
`step` blocks). The driver matches steps by `step_id`, so order carries no meaning, and a
`step_id` cannot appear twice. A step's driver input is `steps[id]` as `data` and
`sensitive_steps[id]` as `sensitive_data`. A step may appear in either object or in both; the two
are merged per step, and on a field present in both, `sensitive_steps` wins. A form without
fields is answered by `{}`.

| Computed | |
|---|---|
| `id` | `entry_id` |
| `title`, `state`, `disabled_by` | from `config_entries/get` |

- **Create:** run the config-flow driver, then read the new entry for the computed attributes.
- **Read:** `config_entries/get` filtered by `domain`, matched on `entry_id`. If gone → remove
  from state. Inputs are never compared.
- **Delete:** `DELETE /api/config/config_entries/entry/<id>`. An entry that is already gone is
  not an error. If HA answers `require_restart: true`, the delete succeeds with a warning that HA
  needs a restart to finish removing the integration.
- **Import:** by `entry_id`, read across all domains. The steps cannot be recovered, so after
  import they are null and the user writes them. The next plan is an in-place update that only
  records them in state; nothing is sent to HA. After that, changing them forces replacement as
  usual. (A null `steps` in state marks the import, since `steps` is required otherwise.)

## `homeassistant_integration_subentry`

A config subentry is part of a config entry that the integration lets the user add, change, and
remove on its own: a plane of `forecast_solar`, a conversation agent of an AI integration, an MQTT
device. It is created by a **subentry flow**, which works like a config flow. Its input data cannot
be read back either.

```hcl
resource "homeassistant_integration_subentry" "kitchen_agent" {
  entry_id      = homeassistant_integration.anthropic.id
  subentry_type = "conversation"
  steps = {
    init = { name = "Kitchen assistant", recommended = true }
  }
}
```

| Argument | Type | |
|---|---|---|
| `entry_id` | string | required, forces replacement |
| `subentry_type` | string | required, as the integration names it (`plane`, `conversation`, …); forces replacement |
| `steps` | as for `homeassistant_integration` | required, at least one step; reconfigure, else forces replacement |
| `sensitive_steps` | as for `homeassistant_integration`, sensitive | optional; reconfigure, else forces replacement |

On create, `steps` and `sensitive_steps` work exactly as for `homeassistant_integration`
(ADR-0021). On reconfigure they answer by fields; see **Update**.

| Computed | |
|---|---|
| `id` | `<entry_id>/<subentry_id>` |
| `subentry_id` | assigned by HA |
| `title` | chosen by the integration's flow; unknown in a plan that reconfigures |
| `unique_id` | the integration's unique ID of the subentry, or null; unknown in a plan that reconfigures |
| `supports_reconfigure` | whether the subentry type has a reconfigure flow, from `supported_subentry_types` |

HA API (the same in 2026.5 and 2026.10):

```
WS   config_entries/subentries/list    {entry_id}               → [{subentry_id, subentry_type, title, unique_id}]
                                                                   unknown entry → not_found
WS   config_entries/subentries/delete  {entry_id, subentry_id}  unknown entry or subentry → not_found
WS   config_entries/get_single         {entry_id}               → {config_entry: {…, supported_subentry_types:
                                                                     {<type>: {supports_reconfigure: bool}}}}
REST POST /api/config/config_entries/subentries/flow  {"handler": [entry_id, subentry_type]}
                                                                   unknown subentry_type → 404
REST POST /api/config/config_entries/subentries/flow  {"handler": [entry_id, subentry_type], "subentry_id": id}
                                                                   starts the reconfigure flow
REST POST|DELETE /api/config/config_entries/subentries/flow/<flow_id>
```

The config-flow driver answers the subentry flow with the steps. The flow ends with
`create_entry`, whose result names neither the subentry nor its ID, or with `abort`. A reconfigure
flow ends with the abort `reconfigure_successful`.

- **Create:** list the entry's subentries (an unknown entry fails here, before any flow), run the
  subentry flow, and list them again. The new subentry is the one of `subentry_type` that was not
  there before; if there are several, the one with the title from `create_entry`. Flows on the
  same entry run one at a time within the provider, so parallel creates cannot mistake each
  other's subentries. An `abort` is an error with HA's reason, as for config flows.
  Then `config_entries/get_single` reads `supports_reconfigure`; if that fails, the subentry is
  kept with the flag null (a warning), and the next refresh reads it.
- **Read:** `config_entries/subentries/list`, matched on `subentry_id`, and
  `config_entries/get_single` for `supports_reconfigure`. If the subentry or its config entry is
  gone → remove from state. Inputs are never compared.
- **Delete:** `config_entries/subentries/delete`. A subentry or entry that is already gone is not an
  error. HA removes the subentry's devices and entities with it.
- **Import:** by `<entry_id>/<subentry_id>`. As for `homeassistant_integration`, the steps are null
  after import, and the next plan only records them.
- **Update:** changed `steps` or `sensitive_steps` run the subentry's **reconfigure flow** if
  `supports_reconfigure` is true in state. It changes the subentry's data in place: the
  `subentry_id`, and with it the subentry's devices and entities, stay. Types without a reconfigure
  flow, or with the flag null, are replaced, as for `homeassistant_integration` (ADR-0014). The
  plan marks `title` and `unique_id` unknown, because the flow may change them, and the update
  reads them back afterwards. Right after an import the steps are only recorded, as above.

Reconfigure flows show the fields of the create flow, but not under the same `step_id`s
(`forecast_solar`: `user`, then `reconfigure`), and not always all of them (an AI integration's
`init` drops `name`). So the reconfigure flow answers each form by its fields, from the same steps:

```
ReconfigureAnswer(steps):
  on every form:
    a field takes its value from steps[form.step_id], else from any step that has it
    fields the form does not have are left out; so are fields no step has (HA keeps their value)
    a field that two other steps set to different values, and steps[form.step_id] lacks → abort + error
    a required field without default that no step has → abort + error
    the same step_id with the same fields as an earlier form → abort + error (no loops)
```

Any other abort reason, or a `create_entry`, is an error; HA then has not changed the subentry.

Errors: an unknown `entry_id` or `subentry_type` gets a hint naming the attribute. The abort
`already_configured` (a subentry with the same `unique_id`) suggests importing with
`<entry_id>/<subentry_id>`.

**Testing.** Unit tests against fakes; acceptance test with `forecast_solar`, whose config flow
creates the entry with one plane. A second plane needs an API key in the entry's options, so the
test checks that abort, imports the first plane, reconfigures it in place (same `subentry_id`, new
title), deletes it outside Tofu so it is created again, and reconfigures the new one. Replacement
of a type without a reconfigure flow is unit-tested only.

## `data.homeassistant_integration`

Arguments: `domain` (required) and `title` (optional). It must match exactly one entry; no match
and several matches are errors that list the candidates.
Computed: `id`/`entry_id`, `title`, `state`, `disabled_by`.

## Typed integrations

`homeassistant_esphome` and `homeassistant_mqtt` have typed attributes instead of `steps`. Their
config flows show different forms depending on the device, on HA's own state, and on the HA
release, so the typed resources do not name steps. They answer every form from one pool of
field values (see *Answering forms from fields*). Hue and Z-Wave JS are not planned.

The schemas below come from each integration's `config_flow.py` in HA 2026.4 and were checked
against 2026.5 (the oldest release of the support window), 2026.9 and 2026.10.

Common to both:

- **Computed:** `id` (`entry_id`), `title`, `state`, `disabled_by`, as for `homeassistant_integration`.
- **Read, delete, import:** as for `homeassistant_integration`, with the domain fixed. Importing an
  entry of another domain fails as "not found".
- **Connection data** is sent through the config flow. Changing it runs a **reconfigure flow**
  (both integrations implement one) and keeps the entry; attributes the reconfigure flow cannot
  change force replacement.
- **Options** are sent through the **options flow**, after the config flow on create, and again
  on update when they change. The options flow always shows a single form.
- Neither data nor options can be read back (ADR-0014), so drift is only "entry is gone".
- **Update** does not read the entry again: a reconfigure reloads it, so `state` changes while
  the apply runs. The next refresh reads it.
- **After an import** the inputs are null in state. The first update only records them, like
  `homeassistant_integration`. Nothing is sent to HA, and no attribute forces replacement.

### Answering forms from fields

```
FieldAnswer(values, files):
  on every form:
    fields = form.data_schema; a field of type "expandable" (a section) has its own schema
    answer  = each field present in values (a section → a nested object of its own fields)
    a file field (selector "file") present in files → POST /api/file_upload, answer the file_id
    a required field without a default that values lacks → abort + error naming it
    the same step_id with the same fields as an earlier form → abort + error (no loops)
```

Fields that `values` lacks are left out, so HA applies its defaults. A step may be shown several
times as long as its fields change; MQTT before 2026.9 relies on that (see below). The driver
takes this as an alternative to the step list; `homeassistant_integration` keeps its steps.

The options flow uses the same driver on `/api/config/config_entries/options/flow`, started with
`{"handler": entry_id}`. It ends with `create_entry`; `abort` is an error.

### `homeassistant_esphome`

```hcl
resource "homeassistant_esphome" "kitchen" {
  host      = "kitchen-sensor.local"
  noise_psk = var.kitchen_api_key
}
```

| Argument | Type | Flow field | |
|---|---|---|---|
| `host` | string | `host` | required; reconfigure |
| `port` | number | `port` | default `6053`; reconfigure |
| `noise_psk` | string, sensitive | `noise_psk` | optional; forces replacement |
| `password` | string, sensitive | `password` | optional, the deprecated API password; reconfigure |
| `allow_service_calls` | bool | option `allow_service_calls` | default `false`; options flow |
| `subscribe_logs` | bool | option `subscribe_logs` | default `false`; options flow |

Config flow (`user` → [`encryption_key`] → [`authenticate`] → `create_entry`):

- `user`: `host` (required), `port` (default 6053).
- `encryption_key`: `noise_psk` (required). Shown only when the device needs encryption and HA
  has no key for it from the ESPHome dashboard or its own key storage. A `noise_psk` that HA does
  not ask for is ignored.
- `authenticate`: `password` (required). Shown only when the device uses an API password.
- A device that already has an entry aborts with `already_configured`. A name conflict with
  another entry shows the `name_conflict` menu, which is not supported (error).

Reconfigure flow: starts at `user`, pre-filled from the entry, and keeps the entry's
`noise_psk`. It aborts with `reconfigure_unique_id_changed` if a different device answers at the
new address. A changed key would fail the connection, so `noise_psk` forces replacement instead.

Options form `init`: `allow_service_calls`, `subscribe_logs` (2026.9 adds
`bluetooth_scanning_mode`, which is not offered).

**Testing.** Unit tests only, because a config flow needs a real device. Manual test, with an
ESPHome device that has an API encryption key:

1. Apply a `homeassistant_esphome` with the device's `host` and `noise_psk`. HA shows the device,
   and `state` is `loaded`.
2. Set `allow_service_calls = true` and apply. The plan is an in-place update, and the device's
   options in HA show "Allow the device to perform Home Assistant actions" checked.
3. Change `host` to the device's IP address and apply. The plan is an in-place update, and `id`
   stays the same.
4. Change `noise_psk` to a wrong key. The plan replaces the entry, and the apply fails with
   `invalid_psk` on `encryption_key`.
5. Restore the key, apply, then `tofu destroy`. The entry is gone.

### `homeassistant_mqtt`

```hcl
resource "homeassistant_mqtt" "broker" {
  broker   = "mqtt.home.local"
  port     = 8883
  username = "homeassistant"
  password = var.mqtt_password
  tls = {
    ca_certificate = file("ca.pem")
  }
  birth_message = { topic = "homeassistant/status", payload = "online" }
}
```

| Argument | Type | Flow field | |
|---|---|---|---|
| `broker` | string | `broker` | required; reconfigure |
| `port` | number | `port` | default `1883`; reconfigure |
| `username` | string | `username` | optional; reconfigure |
| `password` | string, sensitive | `password` | optional; reconfigure. Removing it forces replacement: a reconfigure flow keeps the old password when none is sent. |
| `client_id` | string | `client_id` | optional; reconfigure |
| `keepalive` | number | `keepalive` | default `60`, at least 15; reconfigure |
| `protocol` | string | `protocol` | `3.1`, `3.1.1` (default) or `5`; reconfigure |
| `transport` | string | `transport` | `tcp` (default) or `websockets`; reconfigure |
| `ws_path` | string | `ws_path` | default `/`, only with `websockets`; reconfigure |
| `ws_headers` | map(string) | `ws_headers` (JSON text) | optional, only with `websockets`; reconfigure |
| `tls` | object, see below | | optional; null means no TLS; reconfigure |
| `discovery` | bool | option `discovery` | default `true`; options flow |
| `discovery_prefix` | string | option `discovery_prefix` | default `homeassistant`; options flow |
| `birth_message`, `will_message` | object, see below | options `birth_*`, `will_*` | options flow |

`tls`:

| Attribute | Flow field | |
|---|---|---|
| `ca_certificate` | `set_ca_cert = "custom"` + `certificate` (file) | PEM; null means `set_ca_cert = "auto"` (the system's CAs) |
| `client_certificate`, `client_key` (sensitive) | `set_client_cert = true` + `client_cert`, `client_key` (files) | PEM; both or neither |
| `insecure` | `tls_insecure` | default `false`: do not check the broker's host name |

Without `tls`, the flow gets `set_ca_cert = "off"` and `set_client_cert = false`. Files are
uploaded on every flow, because the flow cannot tell the provider whether the old ones match.
An encrypted client key (`client_key_password`) is not supported.

`birth_message` and `will_message` each default to HA's defaults:

| Attribute | Option field | Default (birth / will) |
|---|---|---|
| `enabled` | `birth_enable` / `will_enable` | `true` |
| `topic` | `birth_topic` / `will_topic` | `homeassistant/status` |
| `payload` | `birth_payload` / `will_payload` | `online` / `offline` |
| `qos` | `birth_qos` / `will_qos` | `0` |
| `retain` | `birth_retain` / `will_retain` | `false` |

Config flow: `user` is a menu (add-on or broker) when HA runs under the Supervisor, which is not
supported (error). Otherwise it goes straight to the `broker` form. The form tests the connection
and ends with `create_entry`, or with the error `cannot_connect`. MQTT allows one entry, and a
second aborts with `single_instance_allowed`.

The `broker` form differs across the window:

- **Before 2026.9:** the first form has `broker`, `port`, `username`, `password` and the checkbox
  `advanced_options`. The other fields appear only after a post with `advanced_options = true`,
  and the certificate fields, `ws_path` and `ws_headers` appear only after a post that selects
  them. The resource sets `advanced_options = true` when it has a value the form does not show
  and that differs from HA's default. It then answers each new form until HA accepts it.
- **2026.9:** a single form. `protocol` moves next to `broker`, and the other fields sit in the
  section `other_settings`, where `set_ca_cert` and `set_client_cert` are required.

Field answering handles both. The reconfigure flow shows the same form, pre-filled from the entry,
and ends with the abort `reconfigure_successful`.

Options form `options`: `discovery`, `discovery_prefix`, `birth_enable`, `birth_topic`,
`birth_payload`, `birth_qos`, `birth_retain`, and the same for `will_` (2026.9 adds
`discovery_qos`, which is not offered).

**Testing.** Unit tests for the field values built from the attributes and for answering both
form layouts. Acceptance test against a mosquitto container that shares HA's network namespace,
so HA reaches it on `localhost:1883`.
