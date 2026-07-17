#!/usr/bin/env bash

set -euo pipefail

latest_tag=
while IFS= read -r tag; do
  if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    continue
  fi
  if [[ -z "$latest_tag" ]]; then
    latest_tag=$tag
    continue
  fi

  latest_version=${latest_tag#v}
  candidate_version=${tag#v}
  newest_version=$(printf '%s\n' "$latest_version" "$candidate_version" | sort -V | tail -n 1)
  if [[ "$newest_version" == "$candidate_version" && "$candidate_version" != "$latest_version" ]]; then
    latest_tag=$tag
  fi
done

printf '%s\n' "$latest_tag"
