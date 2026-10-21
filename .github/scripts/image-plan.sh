#!/usr/bin/env bash
# image-plan.sh BASE HEAD OWNER NAME (spec 0015, AC-19)
# Writes two step outputs: build=false when every file changed between BASE
# and HEAD is under deploy/clusters/ (a seal or release commit), else true;
# image=ghcr.io/<lowercased owner>/aiitsupport-<name>:<HEAD>. An unknown BASE
# (a new branch, a force push) builds.
set -euo pipefail
base=$1 head=$2 owner=${3,,} name=$4

build=true
if [[ -n "$base" && ! "$base" =~ ^0+$ ]] && git cat-file -e "$base^{commit}" 2>/dev/null; then
  changed=$(git diff --name-only "$base" "$head")
  if [[ -n "$changed" ]] && ! grep -qv '^deploy/clusters/' <<<"$changed"; then
    build=false
  fi
fi

{
  echo "build=$build"
  echo "image=ghcr.io/$owner/aiitsupport-$name:$head"
} >>"${GITHUB_OUTPUT:-/dev/stdout}"
echo "image $name: build=$build"
