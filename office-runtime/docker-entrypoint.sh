#!/bin/bash
set -euo pipefail

# DocServer's upstream entry point has no JWT_SECRET_FILE contract. Read a
# dedicated file into its child environment; never put the value in argv/logs.
secret_source=${POGO_OFFICE_JWT_SECRET_FILE:-/run/secrets/pogo-office-jwt}
script_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
# The mounted source stays private to Pogo UID 1000. Office's unprivileged ds
# account gets a byte-identical root:ds/0640 copy in its dedicated runtime tmpfs.
# Read the validated copy for both consumers, avoiding a second source read
# between validation and export when an operator rotates the deployment key.
secret_file=$(python3 "$script_dir/prepare-secret.py" -- "$secret_source")
secret=$(cat -- "$secret_file")
export JWT_ENABLED=true JWT_IN_BODY=true JWT_HEADER=Authorization
export JWT_SECRET="$secret"
export POGO_OFFICE_JWT_SECRET_FILE="$secret_file"
unset secret secret_source secret_file
# The engine pulls immutable read-only library documents directly from Pogo.
# Prepare its bounded download/XML limits before the official entry mutates
# unrelated database and JWT settings or starts conversion workers.
python3 "$script_dir/prepare-limits.py"
unset script_dir
# Official startup regenerates api.js from its template and chooses a timestamp
# cache tag unless HASH is set. Supply the manifest identity to that official
# generation path; all nine resulting bytes are still independently verified.
cache_tag=$(/usr/local/bin/pogo-office-node /opt/pogo-office/docker-engine-identity.mjs)
export HASH="$cache_tag"
unset cache_tag
exec /app/ds/run-document-server.sh "$@"
