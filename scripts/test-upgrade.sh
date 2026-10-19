#!/bin/sh
# A real v0.4.0 writer -> current reader/writer -> real v0.4.0 reader.
# No running shop, Telegram token, or financial API is involved.
set -eu
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_dir"
case "${BOT_UPGRADE_REDIS_URL:-}" in
  redis://127.0.0.1:16379|redis://127.0.0.1:16379/0) ;;
  *) echo 'Set BOT_UPGRADE_REDIS_URL to the fixed isolated Redis on 127.0.0.1:16379' >&2; exit 1 ;;
esac
baseline_commit=528b58b082ab91db8d69295015e1cb59c702f32c
if ! git cat-file -e "$baseline_commit^{commit}" 2>/dev/null; then
  git fetch --no-tags --depth=1 origin "$baseline_commit"
fi
baseline_dir=$(mktemp -d "${TMPDIR:-/tmp}/dujiao-bot-upgrade.XXXXXX")
git archive "$baseline_commit" | tar -x -C "$baseline_dir"
baseline_version=$(cd "$baseline_dir" && go run ./cmd/bot --version)
test "$baseline_version" = 0.4.0
printf 'Baseline: v%s (%s)\n' "$baseline_version" "$baseline_commit"
(
  cd "$baseline_dir"
  BOT_TEST_REDIS_URL="$BOT_UPGRADE_REDIS_URL" BOT_TEST_REDIS_PHASE=seed \
    go test ./internal/session -run '^TestRedisAOFRestart$' -count=1 -v
)
go test ./internal/session -run '^TestUpgradeFromV040$' -count=1 -v
(
  cd "$baseline_dir"
  BOT_TEST_REDIS_URL="$BOT_UPGRADE_REDIS_URL" BOT_TEST_REDIS_PHASE=verify \
    go test ./internal/session -run '^TestRedisAOFRestart$' -count=1 -v
)
printf 'Cross-version storage upgrade passed; baseline source retained at %s\n' "$baseline_dir"
