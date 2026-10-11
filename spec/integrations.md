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

## `homeassistant_integration_options`

The options of a config entry, set through its options flow (issue #64). Many integrations keep
their settings there rather than in the entry's data: `utility_meter`, `threshold`, `min_max`,
`template`, `group`, and others. The entry may come from the UI or from
`homeassistant_integration`; this resource never creates or deletes it.

```hcl
resource "homeassistant_integration_options" "frost" {
  entry_id = homeassistant_integration.frost.id
  steps = {
    init = { lower = 2, hysteresis = 0.5 }
  }
}
```

| Argument | Type | |
|---|---|---|
| `entry_id` | string | required, forces replacement; the import ID |
| `steps` | object: `step_id` → object of fields (dynamic) | required, at least one step |

| Computed | |
|---|---|
| `domain` | from `config_entries/get` |

`steps` has the shape of `homeassistant_integration`'s (ADR-0021). There is no
`sensitive_steps`.

**Reading the options.** `config_entries/get` does not return them. The only way to read them is
to start the options flow: its first form carries the current options, as each field's
`description.suggested_value` (or `default`). Aborting the flow (`DELETE`) changes nothing.

```
OptionsFlows.Form(entry_id):
  POST /api/config/config_entries/options/flow  {"handler": entry_id, "show_advanced_options": true}
  "form"  → DELETE …/options/flow/<flow_id>, return the form
  "abort" → error with result.reason
  other   → DELETE + error (unsupported: menus, external steps, progress)

current value of a field = suggested_value if not null, else default if not null, else none
```

Only the first form can be read: reaching a later one means submitting the first, and submitting
the last one saves the options.

**Writing the options.** The options flow is run with the same init. Each form is answered as the
UI does when only some fields are edited: every field keeps its current value, and the fields of
the step with the form's `step_id` replace it. A field set to `null` is left out, which unsets an
optional option. A section is merged field by field. A form whose `step_id` has no step keeps its
current values. The same `step_id` twice is an error (no loops), and so is a field that the form
does not have, before anything is submitted. The flow ends with `create_entry`; `abort` is an
error. Leaving fields out of the answer would not keep them: options flows built on
`SchemaOptionsFlowHandler` remove optional options that a submit omits.

- **Field-granular:** only the fields in `steps` are managed (like the settings resources,
  ADR-0010). Other options keep their values, whoever set them.
- **Create:** the entry must exist and have an options flow (`supports_options`), else error.
  Then as update.
- **Update:** read the first form. A field in the first step that the form does not have is an
  error. If `steps` has only the first form's step and each of its fields already has its value,
  nothing is submitted; otherwise run the options flow.
- **Read:** entry gone → remove from state. Otherwise read the first form and replace the fields of
  its step in `steps` with their current values (none → `null`). Fields the form does not have,
  and steps other than the first form's, cannot be read and keep their state values. Values are
  compared by semantic equality (ADR-0006), so `5` and `5.0` are no drift. Each refresh starts and
  aborts one options flow.
- **Delete:** leaves the options as they are. HA cannot tell which values they had before.
- **Import:** by `entry_id`. `steps` is null in state after the import, so the read fills it with
  every field of the first form that has a value. A configuration with exactly these fields (what
  `tofu plan -generate-config-out` writes) plans no changes. Leaving fields out afterwards is an
  in-place update that sends nothing, because the remaining fields already have their values.

**Testing.** Unit tests for reading, answering, the unchanged check and unknown fields against a
fake HTTP server. Acceptance test with a `threshold` entry created outside Tofu: import and an
empty plan, a partial update that keeps the other options, drift from outside Tofu, unsetting
with `null`, an unknown field, and destroy leaving the options. An entry of `local_calendar`,
which has no options flow, is an error.

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
