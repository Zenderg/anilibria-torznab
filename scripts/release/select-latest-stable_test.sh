#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
selector="${script_dir}/select-latest-stable.sh"

assert_latest() {
  local name=$1
  local expected=$2
  local releases=$3
  local actual

  actual=$(printf '%b' "$releases" | bash "$selector")
  if [[ "$actual" != "$expected" ]]; then
    printf 'case %s: got %s, want %s\n' "$name" "$actual" "$expected" >&2
    exit 1
  fi
}

assert_latest "publication order cannot lower baseline" "v2.0.0" \
  "v2.0.0\nv1.0.0\n"
assert_latest "semantic version ordering" "v1.10.0" \
  "v1.9.0\nv1.10.0\nv1.2.99\n"
assert_latest "non-stable tags are ignored" "v1.2.3" \
  "v1.2.3-rc.1\nlatest\nv1.2.3\n"
assert_latest "no stable releases" "" \
  "v2.0.0-rc.1\ninvalid\n"
