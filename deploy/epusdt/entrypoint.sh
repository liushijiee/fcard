#!/bin/sh
set -eu
if [ "${1:-}" = bootstrap ]; then
    exec /app/fcard-bootstrap
fi
/app/fcard-bootstrap --check
exec /app/epusdt --config /app/conf/.env http start
