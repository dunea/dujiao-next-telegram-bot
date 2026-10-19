#!/bin/sh
# Only the fixed, fake-credential integration stack is in scope. Never use demo.
set -eu
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_dir"
integration_compose() {
  docker compose -p dujiao-bot-integration -f "$repo_dir/compose.test.yml" "$@"
}
wait_healthy() {
  acceptance_attempt=0
  while [ "$acceptance_attempt" -lt 30 ]; do
    if curl --fail --silent http://127.0.0.1:18444/healthz >/dev/null; then return 0; fi
    acceptance_attempt=$((acceptance_attempt+1))
    sleep 1
  done
  echo 'Integration bot did not recover health' >&2
  return 1
}

integration_compose up -d --build --wait
integration_compose exec -T bot /usr/local/bin/dujiao-bot --version
integration_compose exec -T bot /usr/local/bin/dujiao-compatcheck -user-id 42

BOT_TEST_REDIS_URL=redis://127.0.0.1:16379 BOT_TEST_REDIS_PHASE=seed \
  go test ./internal/session -run '^TestRedisAOFRestart$' -count=1 -v
integration_compose restart redis
wait_healthy
BOT_TEST_REDIS_URL=redis://127.0.0.1:16379 BOT_TEST_REDIS_PHASE=verify \
  go test ./internal/session -run '^TestRedisAOFRestart$' -count=1 -v
BOT_UPGRADE_REDIS_URL=redis://127.0.0.1:16379 sh scripts/test-upgrade.sh

# Always restore this TEST Redis if a failure happens during the outage probe.
trap 'integration_compose start redis >/dev/null 2>&1 || true' EXIT
integration_compose stop redis
health_code=$(curl --silent --output /dev/null --write-out '%{http_code}' http://127.0.0.1:18444/healthz)
test "$health_code" = 503
integration_compose start redis
wait_healthy
trap - EXIT

go run ./cmd/loadtest -mode health -n 2000 -c 50
go run ./cmd/loadtest -mode callback -n 1000 -c 25
go run ./cmd/loadtest -mode callback -n 500 -c 25 -users 1 -expect-rate-limit

# Restart only the bot. Audit volume and Redis state must survive.
integration_compose exec -T bot sh -c 'test -s /var/lib/dujiao-bot/audit.jsonl && test "$(stat -c %a /var/lib/dujiao-bot/audit.jsonl)" = 600'
audit_inode=$(integration_compose exec -T bot stat -c %i /var/lib/dujiao-bot/audit.jsonl)
integration_compose restart bot
wait_healthy
test "$audit_inode" = "$(integration_compose exec -T bot stat -c %i /var/lib/dujiao-bot/audit.jsonl)"
integration_compose exec -T bot sh -c 'test -s /var/lib/dujiao-bot/audit.jsonl && test "$(stat -c %a /var/lib/dujiao-bot/audit.jsonl)" = 600'
echo 'Acceptance passed; integration containers and volumes retained.'
