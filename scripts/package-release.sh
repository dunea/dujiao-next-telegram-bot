#!/bin/sh
# Produces fresh, credential-free release bundles; never overwrites an earlier run.
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_dir"
if [ -n "$(git status --porcelain)" ]; then
  echo 'Release packaging requires a clean committed worktree' >&2
  exit 1
fi
release_version=$(go run ./cmd/bot --version)
case "$release_version" in
  ''|*[!0-9.]*|.*|*..*|*.) echo 'Expected a stable numeric version' >&2; exit 1 ;;
esac
release_dir="$repo_dir/dist/release/v$release_version"
mkdir -p "$repo_dir/dist/release"
mkdir "$release_dir"

for release_target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  release_os=${release_target%/*}
  release_arch=${release_target#*/}
  bundle_name="dujiao-next-telegram-bot_v${release_version}_${release_os}_${release_arch}"
  bundle_dir="$release_dir/$bundle_name"
  mkdir "$bundle_dir"
  CGO_ENABLED=0 GOOS="$release_os" GOARCH="$release_arch" go build \
    -trimpath -ldflags='-s -w' -o "$bundle_dir/dujiao-next-telegram-bot" ./cmd/bot
  go version -m "$bundle_dir/dujiao-next-telegram-bot" > "$bundle_dir/build-info.txt"
  cp .env.example LICENSE README.md CHANGELOG.md ROADMAP.md RELEASE.md "$bundle_dir/"
  mkdir "$bundle_dir/docs"
  cp docs/acceptance-v0.4.0.md "$bundle_dir/docs/"
  COPYFILE_DISABLE=1 tar -czf "$release_dir/$bundle_name.tar.gz" -C "$release_dir" "$bundle_name"
done

# Relative names make checksum verification work after downloading elsewhere.
cd "$release_dir"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum ./*.tar.gz > checksums.txt
else
  shasum -a 256 ./*.tar.gz > checksums.txt
fi
printf 'Release bundles: %s\n' "$release_dir"
