---
status: todo
depends_on: [013]
issues: [83]
adrs: [0025, 0010]
spec: [registries.md, overview.md]
---

# Entity settings keyed by the registry ID; renames in place

From issue #83: renaming an entity, in the UI or in config, breaks `homeassistant_entity_settings`.

## Scope
- `client.EntityEntry` gets `ID` (`id`). `EntityRegistry.Update` takes a `new_entity_id` field
  like any other.
- `homeassistant_entity_settings`: computed `id` (`UseStateForUnknown`); `entity_id` loses
  `RequiresReplace`.
- Read finds the entry by `id`: `get` by `entity_id` first, `list` only if that misses or
  returns another `id`. State without `id` takes it from the `get`.
- Update sends a changed `entity_id` as `new_entity_id`, in the same `update` as the other
  fields. The error for an `entity_id` that is taken suggests `tofu apply -replace`.
- Delete never renames back.
- Docs: renames in config and in the UI, retargeting with `-replace`, and a `moved` block for a
  `for_each` keyed by entity ID (example in `examples/resources/homeassistant_entity_settings/`).
- `CHANGELOG.md`: changing `entity_id` now renames the entity instead of replacing the resource.

## Acceptance criteria
- [ ] Rename in the UI (acceptance test: `update` with `new_entity_id` via WS between steps):
      the plan shows an in-place change of `entity_id`, apply renames it back, and the resource
      stays in state.
- [ ] Rename in the UI, config changed to the new name: the plan is empty.
- [ ] Rename in config: in-place update; the entity has the new ID, its `id` and the configured
      fields are unchanged.
- [ ] Rename to an `entity_id` that another entity has: apply fails with HA's error and the
      `-replace` hint; state is unchanged.
- [ ] Destroy after a rename resets the configured fields on the renamed entity and keeps its
      new entity ID.
- [ ] Upgrade: state written by the released v1.0.0 (acceptance test with
      `ExternalProviders` in the first step, the provider under test in the second) plans empty
      and gets `id` filled in.
- [ ] Unit tests (fake registry): Read falls back to `list` when `get` misses or returns another
      `id`; the entry is gone → removed from state.
- [ ] Import by `entity_id` still works and fills in `id`.

## Notes
- `config/entity_registry/get` has no lookup by `id`, hence the `list` fallback. It runs only
  after a rename.
- HA keeps the `id` when an integration removes an entity and adds it back with the same
  `unique_id`, so such an entity keeps its settings resource too.
