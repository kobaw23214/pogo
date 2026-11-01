#!/bin/sh
# Runtime clients only: no SSH, SQL or FTP server is installed or started.
set -eu

export DEBIAN_FRONTEND=noninteractive
clickhouse_version="${POGO_CLICKHOUSE_CLIENT_VERSION:-26.8.25.14}"

apt-get update
apt-get install -y --no-install-recommends \
  ca-certificates curl gnupg openssl \
  openssh-client postgresql-client default-mysql-client lftp wget \
  iputils-ping iproute2 netcat-openbsd bind9-dnsutils traceroute

# Use the official signed LTS repository, including its native ARM64 packages.
# Keep client/common-static versions together; the latter contains the binary.
curl --fail --silent --show-error --location --retry 3 \
  --connect-timeout 15 --max-time 180 \
  https://packages.clickhouse.com/rpm/lts/repodata/repomd.xml.key \
  --output /tmp/pogo-clickhouse-signing-key
gpg --batch --yes --dearmor \
  --output /usr/share/keyrings/pogo-clickhouse.gpg /tmp/pogo-clickhouse-signing-key
architecture="$(dpkg --print-architecture)"
printf '%s\n' "deb [arch=${architecture} signed-by=/usr/share/keyrings/pogo-clickhouse.gpg] https://packages.clickhouse.com/deb lts main" \
  > /etc/apt/sources.list.d/pogo-clickhouse.list
apt-get update
apt-get install -y --no-install-recommends \
  "clickhouse-client=${clickhouse_version}" \
  "clickhouse-common-static=${clickhouse_version}"

rm -f /tmp/pogo-clickhouse-signing-key
rm -rf /var/lib/apt/lists/*

# Shadow the official client with the compatibility front (see that script's
# header for the ISA history): the official binary's baseline — aarch64
# ARMv8.2 with LSE and LRCPC, x86-64-v3 — is newer than CPUs like Phytium
# FT-2000 under Kylin V10, no pinned compatible build is published, and one
# image must serve every host. /usr/local/bin precedes /usr/bin in PATH, so
# every "clickhouse-client" invocation resolves here first; the wrapper
# execs the real binary untouched wherever it can run.
install -m 0755 /usr/local/lib/pogo/clickhouse-client-compat /usr/local/bin/clickhouse-client

test -x /usr/local/bin/clickhouse-client
sh -n /usr/local/lib/pogo/clickhouse-client-compat

for client in curl wget ssh scp sftp psql mysql mariadb clickhouse-client lftp \
  ping ip nc dig traceroute openssl; do
  command -v "$client" >/dev/null
done
