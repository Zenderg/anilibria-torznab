#!/usr/bin/env bash

set -euo pipefail

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repository_root"

stable_version=$(sed -nE 's/^The current stable release is `(v[0-9]+\.[0-9]+\.[0-9]+)`\..*/\1/p' README.md)
if [[ ! "$stable_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "README must declare exactly one current stable release" >&2
  exit 1
fi

referenced_versions=$(grep -Eoh 'v[0-9]+\.[0-9]+\.[0-9]+' README.md .env.example | sort -u)
if [[ "$referenced_versions" != "$stable_version" ]]; then
  echo "README and .env.example deployment versions disagree:" >&2
  printf '%s\n' "$referenced_versions" >&2
  exit 1
fi

expected_image="IMAGE=ghcr.io/zenderg/anilibria-torznab:${stable_version}"
if ! grep -Fxq "$expected_image" .env.example; then
  echo ".env.example must pin ${expected_image#IMAGE=}" >&2
  exit 1
fi
