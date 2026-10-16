# Changelog

All notable changes to this provider are listed here. Versions follow semver: from v1.0 on,
breaking changes land only in major versions. Before v1.0 they could land in minor versions.
Breaking changes are marked **Breaking** (ADR-0018, ADR-0019).

## Unreleased

- `homeassistant_integration`: a `step_id` in `steps` and `sensitive_steps` may be a list of
  objects, whose entries answer the forms of a config flow that shows the same `step_id` more than
  once, in order (e.g. home-llm's `llama_conversation`, whose two forms are both `user`). An object
  stays valid and answers one form (#69).
- Supports Home Assistant 2026.5 to 2026.10, the six most recent releases (ADR-0003). CI tests
  2026.5.4 and 2026.10.0.
- New resource `homeassistant_integration_subentry`: creates a config subentry, e.g. a
  `forecast_solar` plane or a conversation agent of an AI integration, by answering its subentry
  flow with `steps`, like `homeassistant_integration`. Changed steps run the subentry's
  reconfigure flow where its type has one, which keeps the subentry and its devices and entities;
  other types are replaced. Import by `<entry_id>/<subentry_id>` (#67). `steps` is optional, so
  `tofu plan -generate-config-out` after an import writes a configuration that plans without
  errors (`steps = null`); only creating or replacing a subentry needs `steps` or
  `sensitive_steps`.

## 1.0.0

The first stable release. From here on, breaking changes to resources, attributes or state
land only in a new major version. Upgrading from 0.1.0: read the two **Breaking** entries first.

- **Breaking:** `homeassistant_entity_settings` and `homeassistant_device_settings`:
  `hidden = false` and `disabled = false` now mean "not hidden or disabled by the user". They
  clear a flag the user set, but no longer clear one set by an integration, device, or config
  entry, and destroy leaves a `false` flag alone. Before, create and destroy wrote `null` and
  could re-enable a device that its config entry had disabled, or fail on an entity of a
  disabled device. To enable something an integration disabled, change it in Home Assistant
  (#39).
- **Breaking:** `homeassistant_automation`, `homeassistant_script`, `homeassistant_scene`: create
  fails if an item with the `id` already exists, e.g. one made in the UI, instead of silently
  replacing it. Import it with `tofu import`, or choose another `id` (ADR-0024, #41).
- Data source `homeassistant_entities`: new `hidden` filter, and a `hidden` flag on each entry of
  `entities`, so lookups can skip hidden duplicates (#30).
- Data source `homeassistant_entities`: new `device_id` filter, for telling apart entities with the same
  friendly name on different devices (#30).
- Ctrl-C during a create no longer leaves a half-created floor, area, label, input helper, or
  dashboard behind in Home Assistant: the rollback delete runs even when the run is cancelled.
- Config-flow errors explain what to do: `already_configured` and `single_instance_allowed`
  suggest importing the existing entry, `cannot_connect` and `invalid_auth` point at the network
  and the credentials.
- A create of a floor, area, label, or input helper that fails because Home Assistant is
  unreachable no longer suggests a name clash.
- Deleting an integration warns when Home Assistant needs a restart to finish removing it.
- `examples/demo/`: a demo against a fresh Home Assistant in Docker (one automation template
  for every area, automations from YAML files, drift, dashboards per area), recorded in
  `docs/demo.cast`.
- `examples/`: ten use-case configurations taken from a real home (automations from YAML files
  and templates, entity lookups, floors, areas and labels, scenes and remotes, solar- and
  presence-driven automations, generated dashboards), indexed in `examples/README.md`.
- `homeassistant_automation`, `homeassistant_script`, `homeassistant_scene`: new
  `timeouts = { create }` (default 60s) for the wait until Home Assistant has reloaded the new
  item. The wait polls with backoff (#41).
- `homeassistant_automation`, `homeassistant_script`, `homeassistant_scene`: a refresh no longer
  downloads every state of the instance per resource. Scripts need no lookup, automations and
  scenes read only their own entity's state (#41).
- Home Assistant's validation error for a rejected `config` of an automation, script, scene, or
  dashboard is shown on the `config` attribute (#41).
- Plan-time checks of `url_path`, `entity_id`, config-item `id`, and the fixed-choice and
  numeric-range attributes now use the standard validators of the plugin framework. Their errors
  read "Attribute <name> must …, got: <value>"; the `url_path` and `entity_id` errors still give
  an example of a valid value (#43).
- Fixed: `homeassistant_automation`, `homeassistant_script`, `homeassistant_scene`: a config
  with keys that Home Assistant renames on save (`trigger`, `condition`, `action` at the top
  level, `service` in action steps) no longer shows a diff after every apply. The provider records
  what Home Assistant stored after its own write and compares later reads against it
  (ADR-0023). States from v0.1.0 show the diff once more, until the next apply (#36).
- Fixed: WebSocket client: a command that times out now drops the connection, so a dead
  (half-open) connection no longer makes every later command time out; the next command dials
  again. A dial that times out (Home Assistant down or firewalled) is now reported as
  unreachable, with the `-refresh=false` hint, and commands waiting for a shared dial give up
  when their own context ends (#38).

## 0.1.0

First release with the v0.1 feature set.

- Data source `homeassistant_entity`: state and attributes of one entity.
- Data source `homeassistant_template`: renders a Jinja template on the instance.
- Data source `homeassistant_entities`: lists entities, filtered by domain, area, label, device
  class, or a glob on the friendly name.
- Resource `homeassistant_floor`, with import.
- Resource `homeassistant_automation`, with import. `config` takes the automation as an object,
  e.g. from `yamldecode(file(...))`.
- Resource `homeassistant_input_boolean`, with import.
- Provider functions `tile_card`, `entities_card`, `heading_card` and `grid_section`, which build
  dashboard cards and sections for `homeassistant_dashboard`.
- Provider functions `state_trigger`, `numeric_state_trigger`, `time_trigger`, `state_condition`,
  `time_condition`, `action` and `delay`, which build automation and script steps.
- Resource `homeassistant_area`, with import.
- Resource `homeassistant_script`, with import. `config` takes the script as an object, e.g. from
  `yamldecode(file(...))`; `entity_id` is `script.<id>`.
- Resource `homeassistant_scene`, with import. `config` takes the scene as an object, e.g. from
  `yamldecode(file(...))`.
- Resources `homeassistant_input_number`, `homeassistant_input_text`, `homeassistant_input_select`,
  `homeassistant_input_datetime` and `homeassistant_input_button`, with import and plan-time
  validation.
- Resource `homeassistant_integration`, with import: creates a config entry by answering its
  config flow with `steps`, with secrets in `sensitive_steps`.
- Data source `homeassistant_integration`: one config entry by domain and optional title.
- Data sources `homeassistant_areas` and `homeassistant_area` (by ID or name).
- Data sources `homeassistant_devices`, filtered by integration, manufacturer, model, area, or
  label, and `homeassistant_device` (by ID or name).
- Resource `homeassistant_entity_settings`, with import: name, icon, area, labels, hidden and
  disabled of an entity an integration owns. Only the configured attributes are managed and,
  on destroy, reset.
- Resource `homeassistant_device_settings`, with import: name, area, labels and disabled of a
  device an integration owns, with the same field-granular semantics as
  `homeassistant_entity_settings`.
- Resource `homeassistant_esphome`, with import: adds an ESPHome device by `host`, with
  `noise_psk`, and sets `allow_service_calls` and `subscribe_logs`. Changing `host`, `port` or
  `password` reconfigures the entry in place.
- Resource `homeassistant_mqtt`, with import: the MQTT broker connection, with TLS and
  WebSockets, plus discovery and birth and will messages. Broker changes reconfigure the entry in
  place, and options go through the options flow.

## 0.0.1

First pre-release.

- Provider configuration: `url`, `token`, `insecure` and `timeout`, as attributes or as
  `HOMEASSISTANT_*` environment variables.
- Data source `homeassistant_config`.
- Resource `homeassistant_label`.
- Resource `homeassistant_dashboard`.
