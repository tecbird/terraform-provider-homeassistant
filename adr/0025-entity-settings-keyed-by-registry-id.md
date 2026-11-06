# 25. Entity settings are keyed by the registry ID; `entity_id` is a setting

Date: 2026-11-02 · Status: Accepted · Supersedes the resource-ID point of ADR-0010

## Context

Issue #83: ADR-0010 makes the `entity_id` the ID of `homeassistant_entity_settings`, and changing
it forces replacement. Users rename entities routinely, e.g. after moving or repurposing a device,
or when an update adds entities that break a naming scheme. Both ways of renaming break the
resource:

1. Renamed in the UI: Read gets `not_found` and removes the resource from state. The next apply
   creates it again and waits for the old `entity_id` until it times out.
2. Renamed in config: the replacement resets the configured fields on the old entity, then waits
   for the new `entity_id`, which nothing creates, until it times out.

Every entity registry entry has an `id`, a random hex string that a rename does not change. HA
even keeps it when an integration removes an entity and adds it back with the same `unique_id`
(`deleted_entities`). `config/entity_registry/update` renames an entity with `new_entity_id`, as
the entity settings dialog does. `config/entity_registry/get` looks entries up by `entity_id`
only; `config/entity_registry/list` returns every entry with its `id`.

The options put to the maintainer in the issue:

1. Key the resource on the registry `id` (computed) and make `entity_id` a setting that changes
   in place.
2. As 1, but at plan time, a changed `entity_id` that already belongs to another entity forces
   replacement (retarget), and one that does not renames.
3. Select the entity by `platform` and `unique_id` instead of `entity_id`.
4. Keep the `entity_id` as the ID and only find a renamed entity on Read.

The maintainer chose option 1. Option 2 needs a request at plan time and guesses wrong when the
target entity is created in the same apply. Option 3 asks for values that users rarely know.
Option 4 still plans a replacement that times out.

## Decision

- The resource ID is the entity registry `id`, exposed as the computed attribute `id`.
- `entity_id` stays required. On create, it selects the entity: create waits for it as before
  (ADR-0010) and records its `id`.
- Afterwards, `entity_id` is a setting like `name`. A change in config renames the entity in
  place with `new_entity_id`. A rename made elsewhere shows as drift on `entity_id`, which the
  next apply reverts unless the config is changed to match.
- **Read** finds the entry by `id`: `get` by the `entity_id` in state, and if that misses or
  returns another `id`, `list` and search for the `id`. If no entry has the `id`, the resource is
  removed from state, as before.
- **Destroy** does not rename the entity back. It resets only the other configured fields
  (ADR-0010). Other configuration may already refer to the new name.
- **Import** stays by `entity_id`.
- State written without `id` (before this decision) gets it on the next Read, which looks the
  entity up by its `entity_id` first. No state upgrade is needed.
- To make a resource manage a different entity, rather than rename the one it has, replace it:
  `tofu apply -replace=<address>`.

## Consequences

- Renames in the UI and in config both work and show up in the plan.
- Changing `entity_id` in config means something new: it renamed nothing before, it replaced the
  resource. Renaming to an `entity_id` that another entity has fails in HA, so a config written
  for the old meaning fails loudly rather than renaming silently. This is listed in
  `CHANGELOG.md`.
- A resource keyed by `entity_id` in `for_each` still changes address when the key changes. Such
  a rename needs a `moved` block, and the docs show one.
- A Read after a rename costs one `list` of the whole entity registry.
- `homeassistant_device_settings` is unchanged: its `device_id` already is the registry ID, and
  HA has no device rename that changes it.
