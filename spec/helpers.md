# Helpers

The six `input_*` helpers and `counter`, `timer` and `schedule` are typed resources (ADR-0013 for
the `input_*` ones; the other three follow the same pattern) over the WebSocket collection API:
`<domain>/list|create|update|delete`. Update and delete take `<domain>_id`. IDs are generated
from `name` (ADR-0009, including the optional `id` via create-then-rename and the collision
check).

Common arguments: `name` (required), `id` (optional, computed), `icon` (optional).
Common computed: `entity_id` = `<domain>.<id>`.
Update sends the whole helper, because HA replaces every field on `<domain>/update`. An optional
attribute removed from config is therefore cleared in HA. Attributes that HA fills with a default
have the same default in the schema, so removing them resets them to it: `input_number` `step`
(`1`) and `mode` (`slider`); `input_text` `min` (`0`), `max` (`100`) and `mode` (`text`);
`input_datetime` `has_date` and `has_time` (`false`); `counter` `initial` (`0`), `step` (`1`) and
`restore` (`true`); `timer` `duration` (`0:00:00`) and `restore` (`false`); `schedule` each weekday
(none). The plan-time checks apply these defaults to
unset attributes too.
Read: `<domain>/list`, find by ID; if missing → remove from state. Import: by ID.

| Resource | Extra arguments | Plan-time validation |
|---|---|---|
| `homeassistant_input_boolean` | `initial` (bool) | — |
| `homeassistant_input_number` | `min`, `max` (required), `step`, `initial`, `mode` (`slider`/`box`), `unit_of_measurement` | `min < max`; `initial` within the range; `step > 0` |
| `homeassistant_input_text` | `min`, `max`, `initial`, `pattern`, `mode` (`text`/`password`) | `0 ≤ min ≤ max ≤ 255`; `initial` is `min` to `max` characters long; `pattern` compiles |
| `homeassistant_input_select` | `options` (list, required), `initial` | options non-empty and unique; `initial ∈ options` |
| `homeassistant_input_datetime` | `has_date`, `has_time`, `initial` | at least one of `has_date`/`has_time`; `initial` format matches |
| `homeassistant_input_button` | — | — |
| `homeassistant_counter` | `initial`, `minimum`, `maximum`, `step`, `restore` | `minimum ≤ maximum`; `step ≥ 0`; a configured `initial` within the range |
| `homeassistant_timer` | `duration`, `restore` | `duration` has the format `H:MM:SS` |
| `homeassistant_schedule` | `monday` … `sunday` (lists of `{from, to}`) | each `from` < `to`; ranges of a day in order of `from`, without overlap; time format |

All nine share one generic implementation, parameterised by domain and a field mapping. Its
conversion between attribute values and HA's JSON handles strings, booleans, numbers, lists, and
objects, so a schedule's lists of time ranges need no special code.

`pattern` is a Python regular expression, because HA matches it with `re`. Go's `regexp` (RE2)
rejects valid Python patterns such as lookarounds, so the check compiles it with `regexp2`
instead, after rewriting `(?P=name)` to `\k<name>`. The `input_datetime` `initial` format is
`YYYY-MM-DD` with only a date, `HH:MM[:SS]` with only a time, and `YYYY-MM-DD HH:MM[:SS]` (or
with `T` as the separator) with both.

## `counter`, `timer` and `schedule`

`counter` `minimum` and `maximum` are unset (null) for no bound; HA reports them as `null`. HA
accepts a `step` of 0, so the check only rejects negative values. An unset `initial` is not
checked against the range: HA clamps its default of `0` into it.

`timer` `duration` is stored by HA as `H:MM:SS` (hours without leading zeros, not limited to 23),
whatever spelling it was given in (`00:05:00`, `5:00`, a number of seconds). Any other spelling
would make the value HA reports differ from the config, so the plan-time check accepts only the
stored format.

`schedule` has one attribute per weekday, `monday` to `sunday`, a list of objects `from` and `to`.
HA always stores all seven days, so each defaults to an empty list. Times use the stored format
`HH:MM:SS` (24 hours), for the same reason as the timer's `duration`; `to` may also be `24:00:00`,
the end of the day. HA stores the ranges of a day sorted by start, so the plan-time check requires the list in that
order; another order would read back differently and show as drift. The check mirrors HA's others:
`from` < `to`, and a range must not start before the previous one ends (ranges that meet are fine).

A range in HA can carry a `data` object: custom values (strings, numbers, booleans) that show up in
the entity's attributes while the range is active. It is an optional string attribute holding JSON,
written with `jsonencode(...)`, so an update sends it and never clears what HA has. HA's own
numbers keep their type (`1` stays an integer, `1.5` a float). The plan-time check requires a JSON
object of strings, numbers and booleans, which is what HA accepts. The attribute uses the custom
type `dyntype.JSONStringType`, which compares by semantic equality like dynamic config (ADR-0006):
key order, whitespace and number spelling are not drift. It is a string rather than a dynamic
attribute (ADR-0005) because the framework does not support dynamic attributes inside lists.
