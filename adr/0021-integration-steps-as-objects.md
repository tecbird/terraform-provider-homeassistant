# 21. Integration steps are two dynamic objects keyed by `step_id`

Date: 2026-10-04 · Status: Accepted · Supersedes the step-block point of ADR-0014 · Replacement point superseded by ADR-0028

## Context

ADR-0014 gives `homeassistant_integration` ordered `step` blocks, each with a `step_id`, a plain
`data` map, and a sensitive `sensitive_data` map, with both maps holding dynamic values.
terraform-plugin-framework (v1.19) rejects dynamic attributes inside blocks and collections:
"Dynamic types inside of collections are not currently supported". The agent building ticket 025
found this, and the maintainer chose among these options on PR #22:

1. Two dynamic objects keyed by `step_id`: `steps` and `sensitive_steps`.
2. Keep the blocks, with `data` and `sensitive_data` as JSON strings (`jsonencode` on every
   step).
3. Keep the blocks, with `map(string)` data. Numbers and booleans would have to be coerced via
   the form's `data_schema`, and lists or multi-selects could not be expressed at all.

## Decision

Option 1:

```hcl
resource "homeassistant_integration" "cloud" {
  domain          = "my_cloud"
  steps           = { user = { username = "me@example.com" } }
  sensitive_steps = { user = { password = var.cloud_password } }
}
```

- Both attributes are dynamic objects that map a `step_id` to an object of form fields. Field
  values keep their types.
- `sensitive_steps` is marked sensitive. For each `step_id`, the two objects are merged before
  the step is submitted; on a field present in both, `sensitive_steps` wins. A step may appear in
  either object or in both.
- A form without fields is answered by an empty object, e.g. `confirm = {}`.
- Order carries no meaning, because the config-flow driver matches steps by `step_id`, and each
  `step_id` appears once.
- Any change to either attribute forces replacement, as ADR-0014 already says for inputs.

The rest of ADR-0014 stands.

## Consequences

- Plain HCL with full typing, and only the secret fields are hidden in plan output.
- The configuration can't express a flow that needs different data for two visits to the same
  `step_id`. No known integration needs that.
- After `tofu import`, adding the steps to the configuration produces one in-place update. It
  only records the steps in state and sends nothing to HA, because a configured attribute must
  appear in the plan.
