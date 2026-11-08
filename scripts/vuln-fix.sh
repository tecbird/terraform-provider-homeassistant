#!/usr/bin/env bash
# Bump every module that fails a security gate to the lowest version that fixes it (ADR-0027).
#
#   scripts/vuln-fix.sh <trivy.json> <govulncheck.json> <out-dir>
#
# Reads the findings the gates in .github/workflows/security.yml fail on:
#   - Trivy (`--format json`): CRITICAL or HIGH with a fixed version;
#   - govulncheck (`-format json`): vulnerabilities the provider's code calls (a finding whose
#     trace names a function), with a fixed version.
# For each module it picks the lowest version that fixes all of its findings and runs one `go get`
# for all of them, the standard library as `toolchain@go<version>`. Then `go mod tidy`, and a check
# that every module reached its version. Nothing else is updated, so an indirect dependency moves
# only when a gate forces it.
# Writes <out-dir>/title and <out-dir>/body for the commit and the pull request, and nothing if no
# finding needs a bump. Findings that neither gate fails on (a Trivy finding below HIGH that
# govulncheck does not find reachable) and findings without a fix are left alone.
# A module with a `replace` directive is not bumped: the script fails, because `go get` would not
# change the replacement.
set -euo pipefail

trivy_json="${1:?usage: vuln-fix.sh <trivy.json> <govulncheck.json> <out-dir>}"
govuln_json="${2:?usage: vuln-fix.sh <trivy.json> <govulncheck.json> <out-dir>}"
out="${3:?usage: vuln-fix.sh <trivy.json> <govulncheck.json> <out-dir>}"
mkdir -p "$out"
rm -f "$out/title" "$out/body"

# Findings as "<module>\t<fixed versions, comma-separated>\t<advisory>", versions without "v".
# Each scan is parsed on its own, so a broken file fails the script instead of hiding behind the other.
trivy_findings="$(jq -r '.Results[]? | .Vulnerabilities[]?
  | select(.Severity == "CRITICAL" or .Severity == "HIGH")
  | select((.FixedVersion // "") != "")
  | [.PkgName, .FixedVersion, .VulnerabilityID] | @tsv' "$trivy_json")"
govuln_findings="$(jq -r 'select(.finding) | .finding | select(.trace[0].function != null)
  | select((.fixed_version // "") != "")
  | [.trace[0].module, .fixed_version, .osv] | @tsv' "$govuln_json")"
findings="$(printf '%s\n%s\n' "$trivy_findings" "$govuln_findings" | sed '/^$/d' \
  | sed -E 's/\tv?([0-9])/\t\1/; s/, v/, /g; s/\tgo([0-9])/\t\1/')"
[[ -n "$findings" ]] || exit 0

# current <module>: the version in use, without "v" (stdlib: the toolchain, else the go line).
current() {
  if [[ "$1" == stdlib ]]; then
    go mod edit -json | jq -r '(.Toolchain // ("go" + .Go)) | ltrimstr("go")'
  else
    go list -m -f '{{.Version}}' "$1" | sed 's/^v//'
  fi
}

# Versions compare in Go's semantic-version order: a pre-release ("1.2.3-rc.1") sorts before its
# release. `sort -V` sorts "~" first, so the first "-" becomes "~" for the comparison.
key() { local t='~'; echo "${1/-/$t}"; }
# newer <a> <b>: whether version a is above b. atleast <a> <b>: whether a is b or above.
newer() {
  local a b
  a="$(key "$1")" b="$(key "$2")"
  [[ "$a" != "$b" && "$(printf '%s\n%s\n' "$a" "$b" | sort -V | tail -1)" == "$a" ]]
}
atleast() { ! newer "$2" "$1"; }

declare -A target=() advisories=() from=() fixes=()
while IFS=$'\t' read -r module fixed advisory; do
  if [[ ! -v "from[$module]" ]]; then
    if [[ "$module" != stdlib && -n "$(go list -m -f '{{if .Replace}}replaced{{end}}' "$module")" ]]; then
      echo "vuln-fix: $module has a replace directive; bump it by hand" >&2
      exit 1
    fi
    from[$module]="$(current "$module")"
  fi
  fixes[$module]+="$(tr ',' ' ' <<<"$fixed" | tr -s ' ')"$'\n'
  case " ${advisories[$module]:-} " in
    *" $advisory "*) ;;
    *) advisories[$module]="${advisories[$module]:+${advisories[$module]} }$advisory" ;;
  esac
done <<<"$findings"

# fixes <version> <fixed versions>: whether a version fixes a finding. The reports list one fixed
# version per release branch (1.2.5 and 1.3.2), without affected ranges, so a version counts as
# fixed when it is at or above the highest fix, or at or above a fix on its own major.minor line.
line_of() { cut -d. -f1,2 <<<"$1"; }
fixes() {
  local v="$1" f
  shift
  for f in "$@"; do
    if atleast "$v" "$f" && [[ "$(line_of "$v")" == "$(line_of "$f")" ]]; then return 0; fi
  done
  atleast "$v" "$(printf '%s\n' "$@" | sort -V | tail -1)"
}

# The lowest version that fixes every finding of a module: start at the current version (which
# every finding affects) and raise it to the next fix of any finding it does not fix, until all are
# fixed. Raising for one finding can land on a branch where another is fixed only later; the loop
# goes on until none is left.
for module in "${!from[@]}"; do
  v="${from[$module]}" changed=1
  while ((changed)); do
    changed=0
    while read -r line; do
      [[ -n "$line" ]] || continue
      # shellcheck disable=SC2086 # the fixed versions of one finding, space-separated
      if [[ "$v" != "${from[$module]}" ]] && fixes "$v" $line; then continue; fi
      for f in $(tr ' ' '\n' <<<"$line" | sort -V); do
        if newer "$f" "$v"; then v="$f" changed=1; break; fi
      done
    done <<<"${fixes[$module]}"
  done
  [[ "$v" == "${from[$module]}" ]] || target[$module]="$v"
done
[[ ${#target[@]} -gt 0 ]] || exit 0
modules="$(printf '%s\n' "${!target[@]}" | sort)"
before="$(go list -m -f '{{.Path}} {{.Version}}' all)"
# One `go get` for all of them, so Go resolves them together and none undoes another's fix.
args=()
for module in $modules; do
  if [[ "$module" == stdlib ]]; then args+=("toolchain@go${target[$module]}"); else args+=("$module@v${target[$module]}"); fi
done
go get "${args[@]}"
go mod tidy
for module in $modules; do
  if ! atleast "$(current "$module")" "${target[$module]}"; then
    echo "vuln-fix: $module is at $(current "$module"), not ${target[$module]}" >&2
    exit 1
  fi
done
# Modules Go raised too, because a bumped one requires their newer versions.
along="$(comm -13 <(sort <<<"$before") <(go list -m -f '{{.Path}} {{.Version}}' all | sort) \
  | grep -vxF -f <(for m in $modules; do echo "$m v${target[$m]}"; done) || true)"

name() { if [[ "$1" == stdlib ]]; then echo "the Go toolchain"; else echo "$1"; fi; }
count="$(wc -l <<<"$modules")"
if [[ "$count" -eq 1 ]]; then
  m="$modules"
  echo "chore(deps): bump $(name "$m") from ${from[$m]} to ${target[$m]} (security)" >"$out/title"
else
  echo "chore(deps): bump $count modules for security fixes" >"$out/title"
fi
{
  echo "Bumps what the security gates (Trivy, govulncheck) fail on to the lowest fixed version"
  echo "(ADR-0027). Nothing else is updated."
  echo
  echo "| Module | From | To | Advisories |"
  echo "|---|---|---|---|"
  for m in $modules; do
    echo "| $(name "$m") | ${from[$m]} | ${target[$m]} | ${advisories[$m]} |"
  done
  if [[ -n "$along" ]]; then
    echo
    echo "Raised along with them, because the bumped modules require these versions:"
    echo
    while read -r line; do echo "- $line"; done <<<"$along"
  fi
} >"$out/body"
