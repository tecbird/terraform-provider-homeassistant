# 10. Settings resources are field-granular overlays

Date: 2026-10-03 · Status: Accepted · Entity resource ID superseded by ADR-0025

## Context

Integrations own entities and devices, even when the provider created the config entry that
produced them. Settings resources only edit their registry entries (name
override, icon, area, labels, hidden, disabled). The original spec called these resources
`*_customization`, but in HA "customize" is a different, legacy YAML feature. The original spec
also reset *all* fields on destroy, and it did not handle entities that the integration has not
created yet.

## Decision

- The resources are named `homeassistant_entity_settings` and `homeassistant_device_settings`,
  after the "Entity settings" dialog in the HA UI. The resource ID is the `entity_id` or
  `device_id`.
- **Field-granular:** only attributes set in config are managed. Unset attributes are
  `Optional`, not `Computed`, and are never compared or written. A `labels` value, when set, is
  authoritative for that entry.
- **Create** waits until the registry entry exists, polling with a configurable timeout (default
  60s), then applies the set fields. Read-only facts (`platform`, `device_id`, `original_name`, …)
  are exposed as computed attributes.
- **Destroy** resets only the attributes set in config to HA's defaults (`null` or empty). It
  never deletes. If the entry no longer exists, destroy succeeds without doing anything.

## Consequences

- The UI, other configurations, and Tofu can share one entity without conflict, as long as they
  touch different fields.
- Values that were set before Tofu took over are not restored on destroy. This is documented.
