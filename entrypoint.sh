#!/bin/sh
set -eu

data_file="${TIGERBEETLE_FILE:-/var/lib/tigerbeetle/0_0.tigerbeetle}"

mkdir -p "$(dirname "$data_file")"

if [ ! -s "$data_file" ]; then
    tigerbeetle format \
        --cluster=0 \
        --replica=0 \
        --replica-count=1 \
        --development \
        "$data_file"
fi

exec tigerbeetle start \
    --addresses=0.0.0.0:3000 \
    --development \
    --cache-grid="${TIGERBEETLE_CACHE_GRID:-256MiB}" \
    "$data_file"
