# Purpose: classify immutable release-tag lookups without treating operational
# registry failures as proof that a tag is absent.

registry_ref_state() {
  local ref=$1
  local output

  if output=$(docker buildx imagetools inspect "$ref" 2>&1); then
    printf '%s\n' "exists"
    return 0
  fi

  if grep -Fqi "$ref: not found" <<< "$output" ||
     grep -Eqi 'manifest unknown|(^|[^[:alnum:]])404[[:space:]]+not found([^[:alnum:]]|$)' <<< "$output"; then
    printf '%s\n' "absent"
    return 0
  fi

  printf 'registry lookup failed for %s:\n%s\n' "$ref" "$output" >&2
  return 1
}
