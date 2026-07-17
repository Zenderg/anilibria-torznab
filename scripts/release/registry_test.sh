#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
source "${script_dir}/registry.sh"

ref=ghcr.io/zenderg/anilibria-torznab:v9.9.9

docker() {
  case "$REGISTRY_TEST_CASE" in
    exists)
      printf '%s\n' "Name: ${ref}"
      return 0
      ;;
    ref-not-found)
      printf 'ERROR: %s: not found\n' "$ref" >&2
      return 1
      ;;
    manifest-unknown)
      printf '%s\n' "ERROR: manifest unknown: manifest unknown" >&2
      return 1
      ;;
    http-404)
      printf '%s\n' "ERROR: unexpected status from HEAD request: 404 Not Found" >&2
      return 1
      ;;
    authentication)
      printf '%s\n' "ERROR: denied: permission_denied" >&2
      return 1
      ;;
    dns)
      printf '%s\n' "ERROR: dial tcp: lookup ghcr.io: no such host" >&2
      return 1
      ;;
    rate-limit)
      printf '%s\n' "ERROR: too many requests: rate limit exceeded" >&2
      return 1
      ;;
    timeout)
      printf '%s\n' "ERROR: request canceled while waiting for connection" >&2
      return 1
      ;;
    *)
      printf 'unknown test case: %s\n' "$REGISTRY_TEST_CASE" >&2
      return 2
      ;;
  esac
}

assert_state() {
  local scenario=$1
  local expected=$2
  local actual

  REGISTRY_TEST_CASE=$scenario
  actual=$(registry_ref_state "$ref")
  if [[ "$actual" != "$expected" ]]; then
    printf 'case %s: got %s, want %s\n' "$scenario" "$actual" "$expected" >&2
    exit 1
  fi
}

assert_failure() {
  local scenario=$1

  REGISTRY_TEST_CASE=$scenario
  if registry_ref_state "$ref" >/dev/null 2>&1; then
    printf 'case %s: operational failure was accepted\n' "$scenario" >&2
    exit 1
  fi
}

assert_state exists exists
assert_state ref-not-found absent
assert_state manifest-unknown absent
assert_state http-404 absent

assert_failure authentication
assert_failure dns
assert_failure rate-limit
assert_failure timeout
