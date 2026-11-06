---
status: done
depends_on: [003]
adrs: [0017, 0018, 0026]
spec: []
---

# Upgrade tests: state from the latest release works with the build under test

The README promises that breaking changes to state land only in a new major version. Nothing
tests that: every acceptance test writes its state with the build under test. Came up in the
discussion of issue #83, whose ticket 038 changes the state of `homeassistant_entity_settings`.

## Scope
- `acctest.UpgradeTest(t, config, checks…)`: one `resource.Test` with two steps.
  1. Apply `config` with the latest released provider of the current major version, from the
     registry (`ExternalProviders`, `toelke/homeassistant`, `~> 1.0`). The provider factories are
     set per step, not on the test case, because the two steps use different providers.
  2. Same `config` with `ProtoV6ProviderFactories`: the plan is empty
     (`plancheck.ExpectEmptyPlan`), and the checks hold on the refreshed state.
- One `TestAccXxx_upgrade` per resource, using its basic acceptance config: floor, area, label,
  entity and device settings, automation, script, scene, the six input helpers, dashboard,
  integration, ESPHome, MQTT.
- A resource whose state changes on purpose (e.g. a new computed attribute) checks the new
  value in step 2. A resource that cannot plan empty after an upgrade needs a state upgrader
  or a major version, so it never gets an exception here.
- README, "Acceptance tests": what the upgrade tests do, that they need registry access, and
  that the first release of a new major version updates the constraint.
- No user-visible change, so no `CHANGELOG.md` entry.

## Acceptance criteria
- [x] Every resource has an upgrade test, and all are green against both HA versions in the CI
      matrix and with OpenTofu 1.8. Two exceptions: `homeassistant_entity_settings` gets its
      upgrade test from ticket 038 (PR #86), and `homeassistant_esphome` has no acceptance test
      at all, because it needs a reachable ESPHome device. It shares its implementation
      (`typedIntegration`) with `homeassistant_mqtt`, which has one.
- [x] A deliberately broken state change (e.g. renaming an attribute locally) makes the test of
      that resource fail. Check this once by hand; it is not committed.
- [x] `go test -short ./...` skips the upgrade tests, like every acceptance test.

## Notes
- `~> 1.0` instead of a pinned version: the test then always starts from the state users have
  most recently been given, and a release needs no extra step. The cost is that a new release
  can change the result of an unchanged test.
- `TF_ACC_PROVIDER_HOST=registry.opentofu.org` is already set in CI, so the released provider
  comes from the OpenTofu registry, signed, as users get it.
- Ticket 038 adds the upgrade test for `homeassistant_entity_settings`. If this ticket is done
  first, 038 uses the helper.
- Review of PR #87: a breaking change in a new major version gets no exception either. It
  switches its upgrade test to `acctest.UpgradeTestTo` with the old and the new configuration,
  which must still plan empty (ADR-0026).
