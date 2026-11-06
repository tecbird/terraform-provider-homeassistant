# 26. A breaking change may break configuration, never state

Date: 2026-11-02 · Status: Accepted

## Context

Ticket 039 adds an upgrade test for every resource: apply the basic configuration with the latest
release of the current major version, then plan the same configuration with the build under
test, and require an empty plan. A resource that cannot plan empty "never gets an exception".

A new major version may still change configuration, e.g. rename a required attribute. Its pull
request then fails the upgrade test: the build under test does not accept the old configuration,
and the released provider does not accept the new one. The `Acceptance` checks are required, and
`ReleasedVersion` moves to the new major version only after the release. The review of PR #87
asked how such a change gets merged.

The options put to the maintainer on PR #87:

1. No exception. The breaking pull request ships the migration (a state upgrader, if the state
   schema changes) and gives the upgrade test the new configuration for its second step, which
   must still plan empty.
2. The breaking pull request skips that resource's upgrade test until the release that bumps
   `ReleasedVersion`.
3. Merge despite the failing required check.

The maintainer chose option 1: "If necessary, we may break the code of our users, but we never
break the state. A `tofu plan` should fail loudly if the code is not updated, but after fixing, no
change in Home Assistant should be expected." Option 2 leaves untested the one upgrade path that
needs a test most. Option 3 makes a required check optional in practice and leaves `main` red
until the release.

## Decision

- A breaking change, in a new major version, may require users to change their configuration. It
  never requires a change in Home Assistant, and never requires users to edit state, remove
  resources from it or import them again.
- With configuration that has not been updated, `tofu plan` fails with a diagnostic that says
  what to change. It does not plan a replacement, a destroy, or a change in Home Assistant.
- Once the configuration is updated, the plan is empty. If the state schema changes, the
  resource bumps its schema version and ships a state upgrader.
- The upgrade test gets no exception. `acctest.UpgradeTestTo(t, released, current, checks…)`
  applies `released` with the latest release and plans `current` with the build under test; the
  plan must be empty. The breaking pull request switches the resource's upgrade test to it, with
  the old configuration as `released` and the new one as `current`. The release that bumps
  `ReleasedVersion` switches the test back to `acctest.UpgradeTest` with the new configuration.
- The breaking pull request also tests the diagnostic for the old configuration.
- A resource or attribute that is removed outright, with nothing to migrate to, deletes its
  upgrade test with it. The removal is listed under **Breaking** in `CHANGELOG.md`, with what to
  do instead.

## Consequences

- Every upgrade path that a release ships is tested against a real Home Assistant before merge.
- A breaking change costs a state upgrader and a second configuration in its test. Renames that
  could be done without breaking state are not made "for free" in a major version.
- The README describes this under "Acceptance tests", and its "Releasing" steps switch the tests
  back after the first release of a new major version.
