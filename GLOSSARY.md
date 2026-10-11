# Glossary

Terms used across the ADRs, spec, tickets, and code. When code names a concept, it uses the
term from this file.

## Home Assistant terms

**Home Assistant (HA)** — The home automation server this provider manages. Also called "the
instance" or "the target".

**Long-lived access token** — A bearer token created in an HA user profile. It is the provider's
only authentication method, and it works for both transports.

**Entity** — A single controllable or observable thing in HA, e.g. a light or a temperature
sensor. It has a *state* (a string) and *attributes* (a JSON object).

**Entity ID** — The canonical name of an entity, `<domain>.<object_id>`, e.g.
`light.bedroom_ceiling`.

**Domain** — The part of an entity ID before the dot (`light`, `sensor`, `automation`, …). Usually
also the name of the integration that provides that entity type.

**State** — The live, runtime value of an entity (`"on"`, `"21.5"`). It is not configuration, so
the provider never manages it. See *Runtime state*.

**Device** — A physical or logical unit that groups entities, e.g. an ESPHome board. Devices are
always created by an integration. HA has no API to create a device registry entry directly. The
provider can still cause devices to exist *indirectly*, by creating the config entry they come
from (e.g. `homeassistant_integration`), but it never creates or deletes the device itself.

**Area** — A room or zone. Devices and entities can be assigned to an area.

**Floor** — A building level that groups areas.

**Label** — A cross-cutting tag that can be applied to areas, devices, entities, automations, and
more.

**Registry** — HA's persistent store for one kind of object. This provider uses the area, floor,
label, entity, and device registries.

**Registry entry** — One object in a registry, e.g. the entity registry entry of
`light.bedroom_ceiling`. It holds metadata (name override, area, labels, hidden, disabled), not
state.

**Integration** — An HA component that connects to a service or device family (ESPHome, MQTT,
Hue, …).

**Config entry** — One configured instance of an integration, e.g. "the MQTT broker at
`mqtt.home.local`". It is identified by an HA-assigned `entry_id`. The input data it was created
with (host, password, …) **cannot be read back** via the API.

**Config flow** — The step-by-step wizard (exposed over REST) that creates a config entry. Each
step returns either a `form` (with a `step_id` and data schema), `create_entry`, or `abort`.

**Reconfigure flow** — A config flow started with an existing `entry_id`. It changes a config
entry's data in place. Only some integrations implement it.

**Options flow** — A separate flow that changes a config entry's *options*, as opposed to its
data. It is not used for changing connection data.

**Helper** — A user-defined stateful entity that automations can read and write:
`input_boolean`, `input_number`, `input_text`, `input_select`, `input_datetime`,
`input_button`, `counter`, `timer`, `schedule`.

**Automation** — Triggers, conditions, and actions that HA runs on its own. It is identified by a
config `id`, and its entity ID is derived from its alias — not from its id.

**Script** — A named, reusable action sequence. Its config key is also its object id
(`script.<id>`).

**Scene** — A named set of target entity states that HA can apply.

**Runtime state** — Anything HA changes while running and that users toggle from the UI: entity
state, and whether an automation is enabled. The provider does not manage runtime state. See
ADR-0012.

**Lovelace** — HA's dashboard system. Its WebSocket commands are prefixed `lovelace/`.

**Dashboard** — A Lovelace dashboard. It has *metadata* (`url_path`, title, icon, sidebar
visibility) and a *config* (views → sections → cards).

**`url_path`** — The URL segment of a dashboard. It must contain a hyphen.

**`dashboard_id`** — HA's internal id of a dashboard: `slugify(url_path)`, so `my-home` becomes
`my_home`.

**View** — A tab of a dashboard. **Section** — A grid of cards within a `sections`-type view.
**Card** — A single UI widget.

## Provider terms

**Transport** — How the provider talks to HA: **REST** (`/api/*`, stateless HTTP) or
**WebSocket** (`/api/websocket`, one lazily opened connection per provider instance). Each
operation uses whichever transport HA actually offers for it. See ADR-0004.

**Dynamic config** — A resource attribute that takes an arbitrary HCL value matching HA's own JSON
schema, instead of a typed provider schema. It is used for automations, scripts, scenes, and
dashboards. See ADR-0005.

**Semantic equality** — Treating two dynamic config values as equal when they normalise to the
same JSON (e.g. `5` vs `5.0`, key order). Implemented as a custom attribute type, so a value read
back from HA that means the same thing never shows up as a diff. See ADR-0006.

**Stored baseline** — A hash of a dynamic config as HA stored it right after the provider's own
write, kept in the resource's private state. A later read with the same hash is never drift, even
where HA rewrote the config on save (e.g. renamed old keys). See ADR-0023.

**Builder function** — A provider-defined function (e.g.
`provider::homeassistant::state_trigger(...)`) that returns a plain object for use inside a
dynamic config. Builder functions are optional and give typed ergonomics on top of dynamic config.

**Config item** — An automation, script, or scene: an item that HA's config editor stores through
REST (`/api/config/<domain>/config/<id>`). Its three resources share one implementation.

**User-chosen ID** — An ID the user sets and HA accepts as given: automation, script, scene.
See ADR-0008.

**Generated ID** — An ID HA derives from the name at creation time (`slugify(name)`): floor,
area, label, helpers, dashboards (from `url_path`). See ADR-0009.

**ID collision** — HA appends `_2`, `_3`, … when the slug of a name is already taken. The
provider treats a generated ID that differs from the expected one as an error.

**Create-then-rename** — How the provider honours an optional user-set `id` on a generated-ID
resource. It creates the object using the `id` as its name, then immediately renames it to
`name`.

**Settings resource** — A resource that edits the registry entry of an object it does not own
(`homeassistant_entity_settings`, `homeassistant_device_settings`). Settings resources are
*field-granular*: they manage only the attributes set in config, and on destroy they reset only
those. See ADR-0010.

**Field-granular** — Owning only the attributes present in config. Attributes left unset are
never read into the plan, compared, or written.

**Drift** — A difference between Tofu state and the live HA object that was caused outside Tofu.

**Support window** — The range of HA releases the provider promises to work with: the six most
recent monthly releases. See ADR-0003.

**Headless onboarding** — Setting up a fresh HA container in tests through the onboarding API
(create owner → exchange auth code → mint a token), with no browser involved.
