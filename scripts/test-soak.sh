#!/bin/sh
set -eu

# Isolated fixtures only. Does not load .env or accept a target URL/DSN/token.
case "${DUJIAO_REAL_DB:-}" in
  '') soak_databases='sqlite postgres' ;;
  sqlite|postgres) soak_databases=$DUJIAO_REAL_DB ;;
  *) printf '%s\n' 'DUJIAO_REAL_DB must be sqlite or postgres' >&2; exit 2 ;;
esac
command -v docker >/dev/null
command -v go >/dev/null
if [ "${DUJIAO_REAL_DB:-}" != postgres ]; then command -v sqlite3 >/dev/null; fi
for soak_database in $soak_databases; do
  DUJIAO_E2E_TEST=1 DUJIAO_SOAK_TEST=1 DUJIAO_REAL_DB="$soak_database" \
    go test -race -tags realdujiao ./integration/realdujiao -run '^TestTelegramSustainedLoad$' -count=1 -v -timeout=40m
done
