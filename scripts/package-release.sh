#!/bin/sh
# Produces fresh, credential-free release bundles; never overwrites an earlier run.
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_dir"
release_mode=${1:-stable}
test "$#" -le 1 || { echo 'Usage: package-release.sh [--candidate]' >&2; exit 2; }
case "$release_mode" in stable|--candidate) ;; *) echo 'Usage: package-release.sh [--candidate]' >&2; exit 2 ;; esac
if [ -n "$(git status --porcelain)" ]; then
  echo 'Release packaging requires a clean committed worktree' >&2
  exit 1
fi
release_version=$(go run ./cmd/bot --version)
release_commit=$(git rev-parse HEAD)
if [ "$release_mode" = --candidate ]; then
  printf '%s\n' "$release_version" | LC_ALL=C grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+-dev$' || { echo 'Expected a development version for candidate packaging' >&2; exit 1; }
  release_base="$repo_dir/dist/candidates"
  release_dir="$release_base/${release_version}-${release_commit}"
else
  printf '%s\n' "$release_version" | LC_ALL=C grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || { echo 'Expected a stable numeric version; use --candidate for development' >&2; exit 1; }
  release_base="$repo_dir/dist/release"
  release_dir="$release_base/v$release_version"
fi
mkdir -p "$release_base"
mkdir "$release_dir"
release_go=$(go env GOVERSION)
printf '{"version":"%s","commit":"%s","go_version":"%s","candidate":%s}\n' "$release_version" "$release_commit" "$release_go" "$([ "$release_mode" = --candidate ] && echo true || echo false)" > "$release_dir/release-manifest.json"

# Carry original notices from the modules actually linked into the bot.
go list -deps -f '{{if .Module}}{{.Module.Path}} {{.Module.Dir}}{{end}}' ./cmd/bot > "$release_dir/module-paths.txt"
sort -u "$release_dir/module-paths.txt" > "$release_dir/modules.txt"
notice_dir="$release_dir/third-party"
mkdir -p "$notice_dir/Go"
cp "$(go env GOROOT)/LICENSE" "$notice_dir/Go/"
while IFS=' ' read -r dependency_path dependency_dir; do
  test -n "$dependency_path" || continue
  test "$dependency_path" != github.com/dunea/dujiao-next-telegram-bot || continue
  dependency_notices="$notice_dir/$dependency_path"
  mkdir -p "$dependency_notices"
  notice_found=false
  for dependency_notice in "$dependency_dir"/LICENSE* "$dependency_dir"/LICENCE* "$dependency_dir"/COPYING* "$dependency_dir"/NOTICE*; do
    if test -f "$dependency_notice"; then
      cp "$dependency_notice" "$dependency_notices/"
      notice_found=true
    fi
  done
  if test "$notice_found" = false; then
    printf 'Missing dependency license: %s\n' "$dependency_path" >&2
    exit 1
  fi
  if test -d "$dependency_dir/licenses"; then
    cp -R "$dependency_dir/licenses" "$dependency_notices/"
  fi
done < "$release_dir/modules.txt"

for release_target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  release_os=${release_target%/*}
  release_arch=${release_target#*/}
  bundle_name="dujiao-next-telegram-bot_v${release_version}_${release_os}_${release_arch}"
  bundle_dir="$release_dir/$bundle_name"
  mkdir "$bundle_dir"
  CGO_ENABLED=0 GOOS="$release_os" GOARCH="$release_arch" go build \
    -trimpath -ldflags='-s -w' -o "$bundle_dir/dujiao-next-telegram-bot" ./cmd/bot
  CGO_ENABLED=0 GOOS="$release_os" GOARCH="$release_arch" go build \
    -trimpath -ldflags='-s -w' -o "$bundle_dir/dujiao-compatcheck" ./cmd/compatcheck
  (cd "$bundle_dir" && go version -m ./dujiao-next-telegram-bot > build-info.txt)
  (cd "$bundle_dir" && go version -m ./dujiao-compatcheck > compatcheck-build-info.txt)
  cp .env.example LICENSE THIRD_PARTY_NOTICES.md README.md CHANGELOG.md ROADMAP.md RELEASE.md "$bundle_dir/"
  cp -R "$notice_dir" "$bundle_dir/third-party"
  mkdir "$bundle_dir/docs"
  git ls-files 'docs/*.md' | while IFS= read -r release_doc; do cp "$release_doc" "$bundle_dir/docs/"; done
  COPYFILE_DISABLE=1 tar -czf "$release_dir/$bundle_name.tar.gz" -C "$release_dir" "$bundle_name"
done

# Relative names make checksum verification work after downloading elsewhere.
cd "$release_dir"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum dujiao-next-telegram-bot_*.tar.gz > checksums.txt
else
  shasum -a 256 dujiao-next-telegram-bot_*.tar.gz > checksums.txt
fi
printf 'Release bundles: %s\n' "$release_dir"
(cd "$repo_dir" && go run ./cmd/releasecheck -dir "$release_dir")
