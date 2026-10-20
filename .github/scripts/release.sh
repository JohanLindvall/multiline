#!/usr/bin/env bash
# Called only after CI succeeds on main. Re-runs complete a missing release.
set -euo pipefail

: "${GITHUB_SHA:?GITHUB_SHA must identify the checked commit}"
: "${GH_REPO:?GH_REPO must identify the release repository}"

# An older successful run must not publish over a newer main commit.
remote_head=$(git ls-remote origin refs/heads/main | cut -f1)
if [[ "$remote_head" != "$GITHUB_SHA" ]]; then
  echo 'main has moved; skipping this release.'
  exit 0
fi

git fetch origin --tags
# Ignore prerelease and unrelated tags when selecting a module version.
stable_tags() {
  awk '/^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/' | sort -V
}
tag=$(git tag --points-at "$GITHUB_SHA" | stable_tags | tail -n1)
if [[ -z "$tag" ]]; then
  latest=$(git tag --list | stable_tags | tail -n1)
  if [[ -z "$latest" ]]; then
    tag=v0.0.1
  else
    version=${latest#v}
    IFS=. read -r major minor patch <<< "$version"
    tag="v${major}.${minor}.$((patch + 1))"
  fi
  git config user.name 'github-actions[bot]'
  git config user.email '41898282+github-actions[bot]@users.noreply.github.com'
  git tag -a "$tag" "$GITHUB_SHA" -m "$tag"
  git push origin "$tag"
fi

if gh release view "$tag" --repo "$GH_REPO" > /dev/null 2>&1; then
  echo "Release $tag already exists."
else
  gh release create "$tag" --repo "$GH_REPO" --verify-tag --generate-notes --title "$tag"
fi
