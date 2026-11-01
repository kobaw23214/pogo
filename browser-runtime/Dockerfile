# Pinned Chromium 153 + pixelflux 2.1.0 source, Pogo native additions.
ARG BASE_IMAGE=mcr.microsoft.com/playwright@sha256:eff16c30e6f3f4af0a03fa4b706120d5e9b0891c344a27d64559aff5900a4a27
FROM ${BASE_IMAGE} AS native-build
ARG TARGETARCH
USER root
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates python3-dev python3-venv build-essential clang libclang-dev cmake ninja-build pkg-config nasm \
    libx264-dev libavcodec-dev libavfilter-dev libwayland-dev libxkbcommon-dev libpixman-1-dev \
    libudev-dev libinput-dev libgbm-dev libegl1-mesa-dev libgles2-mesa-dev libdrm-dev libxcb1-dev \
    && rm -rf /var/lib/apt/lists/* && python3 -m venv /opt/wl \
    && /opt/wl/bin/pip install --no-cache-dir setuptools==80.9.0 setuptools-rust==1.12.0 wheel==0.45.1 \
    && curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain 1.89.0
ENV PATH=/root/.cargo/bin:$PATH CARGO_BUILD_JOBS=2
WORKDIR /source
COPY wayland/vendor/pixelflux/ ./
RUN --mount=type=cache,target=/root/.cargo/registry --mount=type=cache,target=/root/.cargo/git \
    --mount=type=cache,id=pogo-pixelflux-target-${TARGETARCH},target=/source/pixelflux/target \
    /opt/wl/bin/python setup.py bdist_wheel --dist-dir /wheels
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS stream-build
ARG TARGETARCH
WORKDIR /stream
COPY wayland/stream/go.mod wayland/stream/go.sum ./
RUN go mod download
COPY wayland/stream/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/pogo-wlstream .
FROM ${BASE_IMAGE}
USER root
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-venv \
    libavcodec60 libavfilter9 libx264-164 libxkbcommon0 libwayland-client0 libwayland-server0 libpixman-1-0 libinput10 \
    && rm -rf /var/lib/apt/lists/* && python3 -m venv /opt/wl
COPY --from=native-build /wheels/ /tmp/wheels/
RUN /opt/wl/bin/pip install --no-cache-dir /tmp/wheels/*.whl && rm -rf /tmp/wheels
COPY --from=stream-build /out/pogo-wlstream /opt/pogo/bin/pogo-wlstream
WORKDIR /app
COPY package.json package-lock.json ./
COPY scripts/patch-playwright-disposable.mjs ./scripts/patch-playwright-disposable.mjs
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund \
    && node scripts/patch-playwright-disposable.mjs
COPY src ./src
COPY wayland/host.py ./wayland/host.py
COPY wayland/vendor/pixelflux/LICENSE* /usr/share/doc/pogo-pixelflux/
RUN chmod -R a+rX /app /opt/pogo/bin /usr/share/doc/pogo-pixelflux
# Real Wayland Chromium uses the unprivileged Chromium namespace sandbox. No
# host desktop, GPU device, VNC, extension capture, or provider credential env.
ENV POGO_BROWSER_HOST=0.0.0.0 POGO_BROWSER_PORT=7788 HOME=/tmp PYTHONUNBUFFERED=1 \
    PIXELFLUX_MANUAL_PLACEMENT=1 WLR_RENDERER=pixman UV_THREADPOOL_SIZE=2 RAYON_NUM_THREADS=2
USER pwuser
EXPOSE 7788
CMD ["node", "src/server.js"]
