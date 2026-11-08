# 28. Steps are optional; only known steps are replaced

Date: 2026-11-08 · Status: Accepted · Supersedes the replacement point of ADR-0021

## Context

ADR-0021 says: "Any change to either attribute [`steps`, `sensitive_steps`] forces replacement".
Its Consequences already make one exception: after `tofu import`, adding the steps records them in
state without a replacement, because Home Assistant cannot return the steps of an existing entry.

`tofu plan -generate-config-out` with an `import` block failed for `homeassistant_integration`:
the generated configuration leaves `steps` out (null after import), and `steps` was required. PR
#76 makes `steps` optional and lets removing both attributes forget the steps in place.
`homeassistant_integration_subentry` (PR #75) works the same way. The review of PR #76 pointed out
that this contradicts ADR-0021.

Home Assistant offers no way to read the steps back (probed on HA 2026.5 and 2026.10): the config
entry APIs return no `data`, diagnostics exist only for some integrations and redact secrets, and
options and reconfigure forms show other fields.

The options put to the maintainer on PR #76:

1. Replace only when the steps are known both before and after. Null → value records the steps,
   value → null forgets them, both in place.
2. Reject value → null at plan time, telling the user to use `ignore_changes` or `tofu state rm`
   and import again. Null → value and `-generate-config-out` would still need this ADR.
3. Value → null forces replacement, literally as ADR-0021 says. Unworkable: a replacement is
   planned as a create, a create without steps fails, so every such plan would fail.

The maintainer chose option 1. Forgetting the steps changes nothing in Home Assistant, it leaves
state as an import does, and it is the only option that lets a user return to the configuration
`-generate-config-out` writes.

## Decision

For `homeassistant_integration` and `homeassistant_integration_subentry`:

- `steps` and `sensitive_steps` are optional. When neither holds a step, the provider does not
  know the steps, as after an import. For the integration, null and `{}` hold no step; for the
  subentry, only null.
- A change to the steps forces replacement only when they are known both in state and in the
  plan. Unknown → known records them in state; known → unknown forgets them. Neither sends anything
  to Home Assistant. A subentry whose type has a reconfigure flow runs that flow instead of being
  replaced.
- Creating an entry, including the create half of a replacement, needs a step; the plan fails
  without one. A changed `domain` or `-replace` with no steps therefore fails at plan time, before
  anything is deleted.

The rest of ADR-0021 stands.

## Consequences

- `tofu plan -generate-config-out` produces a configuration that plans empty.
- Removing the steps from the configuration no longer replaces the entry. A user who wants a new
  entry with new steps changes them, which still replaces it.
- A configuration without steps can't recreate its entry: a `-replace` or a changed `domain` fails
  at plan time with a message asking for the steps.
