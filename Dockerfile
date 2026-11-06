# Pogo 单容器基座：Web/Agent、内置 Skill 运行依赖和通用Skill沙箱。
# Ghidra/CAPA/YARA 等领域程序由上传的Skill包提供，不编入基座镜像。
# pogo-skill-runner 是每次Skill调用使用的短生命周期子进程，不是额外服务。
#
# 构建上下文是本仓库根(独立仓库布局);.dockerignore 负责瘦身。

# ---- Stage 1: Web UI(BUILDPLATFORM=native,vite 产物与架构无关)----
FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS ui
WORKDIR /ui
COPY web/ui/package.json web/ui/package-lock.json ./
RUN npm ci
COPY web/ui/ ./
RUN npm run build

# ---- Stage 2: pogo 二进制(native 构建机 + Go 交叉编译到目标架构)----
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY ./ ./
# CRLF armor: a Windows checkout (or a git operation resetting tracked files)
# hands us scripts whose shebangs read "...python3\r" — the kernel then
# ENOENTs the interpreter and every skill script dies with 127. Whatever the
# working tree says, what gets embedded is LF.
RUN find ./skills/builtin -type f -exec sed -i 's/\r$//' {} +
# web 的 embed 要求 dist 在编译前就位
COPY --from=ui /ui/dist ./web/ui/dist
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/pogo .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/pogo-skill-runner ./cmd/pogo-skill-runner
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/pogo-toolprobe ./cmd/toolprobe

# Code 节点的 JavaScript 运行时必须匹配目标架构，不能复制 BUILDPLATFORM
# UI 阶段的 Node。只带解释器，不开放 npm 或在运行时安装依赖。
FROM node:24-bookworm-slim AS code-runtime

# ---- Stage 3: 运行时 ----
FROM debian:bookworm-slim AS pogo-main
# git:checkpoint 影子仓库;bwrap:OS 沙箱(seccomp profile 见 compose);curl:调试。
# file: 校验下载产物和二进制文件类型。
# python venv 固定在 /opt/venv:内置 docs 技能的脚本(docx-info/pdf-info 等)
# shebang 就是这个路径——基座镜像必须自带自己技能的运行时,精简掉 python 的
# 代价是技能脚本全部 127(实测)。pymupdf 是 docs 的 PDF 引擎;numpy/pandas/
# matplotlib 是通用数据分析三件套——agent 现场写 python 做数据工作是基座的
# 正常用法,环境里没有 numpy 只会让它反复撞 ModuleNotFoundError(实测)。
# 国内网络对 deb.debian.org(Fastly CDN)时常 502/超时,构建时可用
#   --build-arg APT_MIRROR=mirrors.tuna.tsinghua.edu.cn
# 换镜像;默认空 = 官方源,行为不变。
ARG APT_MIRROR=
RUN if [ -n "$APT_MIRROR" ]; then \
        sed -i "s|deb.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources; \
    fi \
    && apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates git curl bubblewrap file python3 python3-venv libstdc++6 libatomic1 zlib1g \
    && python3 -m venv /opt/venv \
    && /opt/venv/bin/pip install --no-cache-dir pymupdf openpyxl numpy pandas matplotlib \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -m -s /bin/bash pogo
ARG POGO_CLICKHOUSE_CLIENT_VERSION=26.8.25.14
COPY scripts/install-network-clients.sh /usr/local/lib/pogo/install-network-clients.sh
COPY scripts/clickhouse-client-compat /usr/local/lib/pogo/clickhouse-client-compat
RUN POGO_CLICKHOUSE_CLIENT_VERSION="$POGO_CLICKHOUSE_CLIENT_VERSION" \
    sh /usr/local/lib/pogo/install-network-clients.sh
# Word 的生成、扩写及批量修改使用 python-docx；在构建阶段预装，
# Agent 可直接使用只读的公共 Python 环境，无需现场 pip 安装。
RUN /opt/venv/bin/pip install --no-cache-dir 'python-docx>=1.2,<2'
COPY --from=build /out/pogo /usr/local/bin/pogo
COPY --from=build /out/pogo-skill-runner /usr/local/bin/pogo-skill-runner
COPY --from=build /out/pogo-toolprobe /usr/local/bin/pogo-toolprobe
COPY --from=code-runtime /usr/local/bin/node /usr/local/bin/node
ENV PATH="/opt/venv/bin:$PATH"

# 预置 $HOME/.pogo 并归 pogo:compose 会把 providers.json bind 到这里,而
# Docker 为 bind 目标自动补建的路径属主是 root——不预置的话,全新 named
# volume 里的 .pogo 会是 root 属主,sessions/memory 全部建不出来(实测)。
RUN install -d -o pogo -g pogo /home/pogo/.pogo \
    && install -d -m 0700 -o pogo -g pogo /var/lib/pogo-skills

# 会话/记忆/偏好都落在 $HOME/.pogo 下,compose 用 named volume 保住它们。
ENV HOME=/home/pogo
USER pogo
WORKDIR /home/pogo
EXPOSE 7777
ENTRYPOINT ["pogo"]
CMD ["-h"]

# Opt-in website release targets. The last stage below keeps the historical
# default image unchanged; select these targets explicitly when packaging.
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS web-service-build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY ./ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/pogo-web-gateway ./cmd/pogo-web-gateway \
    && CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/pogo-web-runner ./cmd/pogo-web-runner

FROM debian:bookworm-slim AS web-service-control
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

FROM web-service-control AS web-service-gateway
COPY --from=web-service-build /out/pogo-web-gateway /usr/local/bin/pogo-web-gateway
USER 1000:1000
ENTRYPOINT ["pogo-web-gateway"]

FROM web-service-control AS web-service-runner
COPY --from=web-service-build /out/pogo-web-runner /usr/local/bin/pogo-web-runner
USER 0:0
ENTRYPOINT ["pogo-web-runner"]

# Standard library only: no pip/npm installation or build step at run time.
# The runner pins the resulting image digest and overrides entrypoint/user;
# its root supervisor drops the application to UID/GID 1000 before execution.
FROM debian:bookworm-slim AS web-service-python
RUN apt-get update && apt-get install -y --no-install-recommends python3 \
    && rm -rf /var/lib/apt/lists/*
LABEL io.pogo.web.profile="python-web" io.pogo.web.python="/usr/bin/python3"
USER 1000:1000
ENTRYPOINT ["/usr/bin/python3"]
CMD ["--version"]

# Node applications use the same trusted Python supervisor and inherited Linux
# seccomp outbound-network filter as Python applications. Only the Node binary
# is copied: npm installation/builds are not part of this runtime profile.
FROM web-service-python AS web-service-node
USER root
RUN apt-get update && apt-get install -y --no-install-recommends libstdc++6 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=code-runtime /usr/local/bin/node /usr/local/bin/node
LABEL io.pogo.web.profile="node-web" io.pogo.web.node="/usr/local/bin/node" io.pogo.web.python="/usr/bin/python3"
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/node"]
CMD ["--version"]

# `docker build .` remains the original Pogo main image.
FROM pogo-main AS release
