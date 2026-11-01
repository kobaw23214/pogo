# The platform profiles select an immutable engine and its audited manifest.
# Keep the existing ARM64 defaults for callers that build this context directly.
ARG OFFICE_ENGINE_IMAGE=onlyoffice/documentserver@sha256:c6e8858f573e56272d618d70505d08cc121189b8510cacad248078ad0b3dbcc6
ARG NODE_RUNTIME_IMAGE=node@sha256:3638d9a6fe4030bd716be989438248074489337ba3275657f93595428be4fc03
FROM ${NODE_RUNTIME_IMAGE} AS node-runtime
FROM ${OFFICE_ENGINE_IMAGE}
ARG TARGETARCH
ARG OFFICE_ENGINE_IMAGE
ARG OFFICE_ENGINE_MANIFEST=engine-manifest.json
COPY --from=node-runtime /usr/local/bin/node /usr/local/bin/pogo-office-node
WORKDIR /opt/pogo-office
COPY engine-proxy.mjs engine-extension.js docker-proxy.mjs docker-engine-identity.mjs healthcheck.mjs ./
COPY ${OFFICE_ENGINE_MANIFEST} ./engine-manifest.json
COPY verify-platform.py ./verify-platform.py
COPY docker-entrypoint.sh prepare-secret.py prepare-limits.py /opt/pogo-office/
COPY docker-supervisor.conf /etc/supervisor/conf.d/pogo-office-proxy.conf
RUN python3 /opt/pogo-office/verify-platform.py \
      --platform "linux/${TARGETARCH}" --upstream-image "${OFFICE_ENGINE_IMAGE}" \
      --runtime-architecture "$(dpkg --print-architecture)" && \
    mkdir -p /var/log/onlyoffice/documentserver/pogo-office-proxy && \
    chown ds:ds /var/log/onlyoffice/documentserver/pogo-office-proxy && \
    chmod 755 /opt/pogo-office/docker-entrypoint.sh && \
    chmod -R a+rX /opt/pogo-office && /usr/local/bin/pogo-office-node --version && \
    python3 --version && \
    python3 -c "import pathlib; [compile(p.read_bytes(), str(p), 'exec') for p in pathlib.Path('/opt/pogo-office').glob('prepare-*.py')]" && \
    install -d -o root -g ds -m 0710 /run/pogo-office-private
# The only published listener is the verified adapter. Raw DocServer :80 stays
# inside this container; do not expose it or use it as the browser engine URL.
EXPOSE 8080
# Keep liveness samples cheap; the grace period covers a five-minute engine
# startup plus SDK verification without coupling Pogo core startup to Office.
HEALTHCHECK --interval=15s --timeout=5s --start-period=360s --retries=3 \
  CMD ["/usr/local/bin/pogo-office-node", "/opt/pogo-office/healthcheck.mjs"]
ENTRYPOINT ["/opt/pogo-office/docker-entrypoint.sh"]
