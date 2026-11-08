# 27. Indirect dependencies move only when a security gate forces it

Date: 2026-11-07 · Status: Accepted

## Context

The Security workflow fails a run on a fixable CRITICAL or HIGH Trivy finding and on a
vulnerability that govulncheck finds in a code path the provider calls. On 2026-11-07, a new HIGH
CVE in `golang.org/x/net` v0.60.0, an indirect dependency, turned the Trivy gate red on every pull
request (#75). The provider never uses the vulnerable package (`x/net/html`); the gate still fails,
and a pull request cannot merge without green checks.

Dependabot did not help:

- Its weekly version updates check only direct Go dependencies (`dependency-type: direct`,
  Dependabot's default for Go modules). A manual run found nothing to do.
- Its security updates also bump indirect dependencies, but only for advisories that GitHub has
  reviewed. The new CVEs were in the Go vulnerability database and Trivy's database, not yet in
  GitHub's. Dependabot alerts and security updates are enabled anyway (2026-11-07).

The options put to the maintainer:

1. `allow: dependency-type: all` in `dependabot.yml`: the weekly update bumps every indirect
   dependency too.
2. Bump by hand whenever a gate fails.
3. A workflow that bumps exactly what fails a gate, to the lowest fixed version, and opens a pull
   request like a Dependabot security update.

The maintainer chose option 3: "I do not want to update indirect dependencies without being forced
by the direct ones, but the vulnerability checking is kinda forcing me to." Option 1 updates
indirect dependencies that nothing forces. Option 2 leaves every pull request red until someone
notices.

The maintainer also chose how that pull request starts its checks, and how much it fixes:

- A pull request opened with a workflow's own token starts no workflows, so its required checks
  would never run. Options: a GitHub App token, a personal access token of the maintainer, or the
  workflow's own token with the workflow starting the checks itself (`workflow_dispatch`). The
  maintainer chose the last one: no secret to keep or renew.
- Fix only what fails a gate, or every known vulnerability that has a fix (`govulncheck -scan
  module`). The maintainer chose only what fails a gate.

## Decision

- Dependabot's weekly Go update stays limited to direct dependencies.
- `.github/workflows/vuln-fix.yml` runs daily, by hand, and after a failed Security run on `main`.
  It runs the gates' scans on `main`, and `scripts/vuln-fix.sh` takes the findings each gate fails
  on: Trivy's CRITICAL and HIGH findings, reachable or not; govulncheck's reachable findings, of any
  severity. For each module they name that has a fix, it picks the lowest version that fixes all of
  its findings, and bumps all of them in one `go get`; the standard library bumps the `toolchain`
  line. Nothing else is bumped, except what Go's minimal version selection raises along with it,
  which the pull request lists. A module with a `replace` directive is not bumped; the run fails.
- Build, unit tests and both gates must pass on the bumped tree. Then the workflow pushes branch
  `deps/vuln-fixes` and opens or updates one pull request from it, as `github-actions[bot]`.
- The job that builds and tests the new dependency code has a read-only token and keeps no
  credentials in the checkout. A second job holds the write token, applies the bump as a patch,
  and runs no Go code: a compromised release can run code during the tests, but cannot push.
- The workflow starts CI, Acceptance tests and Security on that branch with `workflow_dispatch`,
  each one that has no run for the branch's head yet. These workflows accept it, and CI's `rebased`
  check compares a dispatched branch against `main`.
- The pull request is merged like any other: after toelke's LGTM, through `scripts/lgtm-check.sh`.

## Consequences

- A gate that turns red on a new advisory gets a fix pull request within a day, before GitHub
  reviews the advisory, and no indirect dependency moves without a finding.
- A finding that fails neither gate (below HIGH and not reachable) stays open as a code scanning
  alert and is not bumped. So does a finding without a fix: Trivy's gate ignores it (it skips
  unfixed findings), govulncheck's gate still fails on it if it is reachable.
- The repository must allow GitHub Actions to create pull requests ("Allow GitHub Actions to create
  and approve pull requests"). That setting also lets a workflow's token approve pull requests; no
  workflow here does, and merging still needs toelke's LGTM.
- Checks started by `workflow_dispatch` attach to the branch's head commit. GitHub shows them on the
  pull request and counts them for the `main` ruleset, but does not rerun them on its own: a push
  to the branch by anyone but the workflow needs the checks started again (`gh workflow run`).
- If the minimal fix does not build, fails the unit tests, or leaves a gate red (a fix on another
  release branch than the version picked, say), no pull request is opened and the run fails, so the
  problem shows up in the Actions tab instead of as a broken pull request.
- A check workflow whose dispatch failed is started again by the next run, even when the branch is
  unchanged.
