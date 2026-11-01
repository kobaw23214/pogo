# Native amd64/arm64 dependencies; source is copied separately after filtering.
FROM golang:1.26-bookworm AS go
FROM node:24-bookworm-slim AS node
FROM mcr.microsoft.com/playwright:v1.63.0-noble

USER root
RUN apt-get update && apt-get install -y --no-install-recommends \
    bubblewrap build-essential ca-certificates docker.io docker-compose-v2 git python3 python3-venv python3-openpyxl \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m venv --system-site-packages /opt/venv \
    && mkdir -p /work/source /work/artifacts /home/pwuser/bin \
    && chown -R pwuser:pwuser /work /home/pwuser
COPY --from=go /usr/local/go /usr/local/go
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
ENV PATH=/home/pwuser/bin:/usr/local/go/bin:/opt/venv/bin:$PATH \
    HOME=/home/pwuser CGO_ENABLED=1 PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
USER pwuser
WORKDIR /work/source
