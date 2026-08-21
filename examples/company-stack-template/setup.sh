#!/usr/bin/env bash
# Clones (or updates) every service listed in repos.txt into the directory
# layout ensemble.yaml expects. Safe to re-run: existing clones are fetched
# and fast-forwarded rather than re-cloned.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

MANIFEST="${1:-repos.txt}"

if [[ ! -f "$MANIFEST" ]]; then
  echo "manifest not found: $MANIFEST" >&2
  exit 1
fi

while read -r name url branch dir; do
  [[ -z "$name" || "$name" == \#* ]] && continue

  if [[ -d "$dir/.git" ]]; then
    echo "==> $name: already cloned, fetching $branch into $dir"
    git -C "$dir" fetch --quiet origin "$branch"
    git -C "$dir" checkout --quiet "$branch"
    git -C "$dir" pull --quiet --ff-only origin "$branch"
  else
    echo "==> $name: cloning $url@$branch into $dir"
    mkdir -p "$(dirname "$dir")"
    git clone --quiet --branch "$branch" --single-branch "$url" "$dir"
  fi
done < "$MANIFEST"

echo
echo "All services are in place. Next: ensemble up -c ensemble.yaml"
