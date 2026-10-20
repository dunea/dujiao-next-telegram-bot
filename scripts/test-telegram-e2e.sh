#!/bin/sh
set -eu

# Explicit opt-in; never loads .env or addresses a real Telegram/payment service.
case "${DUJIAO_REAL_DB:-}" in
  '') e2e_databases='sqlite postgres' ;;
  sqlite|postgres) e2e_databases=$DUJIAO_REAL_DB ;;
  *) printf '%s\n' 'DUJIAO_REAL_DB must be sqlite or postgres' >&2; exit 2 ;;
esac
command -v docker >/dev/null
command -v go >/dev/null
if [ "${DUJIAO_REAL_DB:-}" != postgres ]; then command -v sqlite3 >/dev/null; fi
for e2e_database in $e2e_databases; do
  DUJIAO_E2E_TEST=1 DUJIAO_REAL_DB="$e2e_database" \
    go test -race -tags realdujiao ./integration/realdujiao -run '^TestTelegram(EndToEnd|AutoDelivery|CallbackRestart)$' -count=1 -v -timeout=20m
done
