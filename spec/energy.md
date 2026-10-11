# Energy

The Energy dashboard preferences, over the WebSocket `energy/*` API. Unlike config entries they
are fully readable, so drift detection works.

## `homeassistant_energy_preferences`

A singleton: an instance has one set of preferences, and its `id` is the constant
`energy_preferences` (also the import ID). All three attributes are dynamic (ADR-0005), take what
`energy/get_prefs` returns unchanged, and are passed to `energy/save_prefs` unchanged. The shape of
a source depends on the HA release (2026.10 rejects the older grid source with `flow_from` and
`flow_to`), so the provider knows nothing of it: write what your instance's
`tofu plan -generate-config-out` produces.

```hcl
resource "homeassistant_energy_preferences" "this" {
  energy_sources = [
    { type = "solar", stat_energy_from = "sensor.pv_energy", config_entry_solar_forecast = null },
  ]
  device_consumption       = [{ stat_consumption = "sensor.dryer_energy" }]
  device_consumption_water = []
}
```

| Argument | Type | |
|---|---|---|
| `energy_sources` | dynamic (semantic equality) | required; list of sources (`grid`, `solar`, `battery`, `gas`, `water`) |
| `device_consumption` | dynamic (semantic equality) | required; list of `{ stat_consumption, … }` |
| `device_consumption_water` | dynamic (semantic equality) | required; list of `{ stat_consumption, … }` |

| Computed | |
|---|---|
| `id` | `energy_preferences` |

Drift is measured against a **stored baseline** (ADR-0023) of the whole document, not of each
attribute: the hash covers all three keys, a key HA does not return counting as `[]`. A read equal
to the baseline keeps all three attributes as written. Any other read is compared per attribute by
semantic equality.

| Operation | Calls |
|---|---|
| Create | `energy/get_prefs` (non-empty preferences → error asking for an import; `not_found` counts as empty) → `energy/save_prefs` → `energy/get_prefs`, stored as the baseline |
| Read | `energy/get_prefs` (`not_found` → remove from state) |
| Update | `energy/save_prefs` with all three keys → `energy/get_prefs` for a new baseline |
| Delete | `energy/save_prefs` with three empty lists; HA cannot delete the preferences |
| Import | ID must be `energy_preferences`; Read does the rest |

`energy/save_prefs` merges the keys it is given, and the provider always gives all three, so a
save replaces the whole document. HA validates the contents on save (`invalid_format`); a rejected
save is a diagnostic on the resource, as HA does not say which key it rejects.

Two resources of this type in one configuration overwrite each other. The provider cannot detect
that.
