#!/bin/sh
set -eu

# Opt-in only: never called by ordinary unit tests or the Mock acceptance stack.
# The harness ignores merchant/Bot credentials, generates its own private config,
# and only accepts a local Docker engine plus sqlite/postgres as the selector.
case "${DUJIAO_REAL_DB:-}" in
  ''|sqlite|postgres) ;;
  *) printf '%s\n' 'DUJIAO_REAL_DB must be sqlite or postgres' >&2; exit 2 ;;
esac
command -v docker >/dev/null
command -v go >/dev/null
if [ "${DUJIAO_REAL_DB:-}" != postgres ]; then command -v sqlite3 >/dev/null; fi

DUJIAO_REAL_TEST=1 go test -race -tags realdujiao ./integration/realdujiao -count=1 -v -timeout=20m
