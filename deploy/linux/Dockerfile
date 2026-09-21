# Compile on the runner's native architecture and cross-compile the binaries
# for the requested image platform. This keeps the arm64 Go compiler out of
# QEMU while preserving the target architecture in the final image.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS go-source

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# Only Go build inputs belong in this layer. Legacy C sources, map data and
# deployment documentation must not invalidate the control-plane compilation.
COPY client/web/runtimeassets/ ./client/web/runtimeassets/
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY server/go/ ./server/go/

ARG TARGETOS
ARG TARGETARCH

FROM go-source AS build-control

RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/ \
    ./cmd/stoneage-gateway ./cmd/stoneage-admin ./cmd/stoneage-operator \
    ./cmd/stoneage-assets-sync

COPY client/web/*.go client/web/index.html ./client/web/

ARG RELEASE_VERSION=dev
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -mod=readonly -trimpath -ldflags="-s -w -X main.releaseVersion=$RELEASE_VERSION" \
    -o /out/web ./client/web && \
    mv /out/web /out/stoneage-web

FROM go-source AS build-ai

RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/ \
    ./cmd/stoneage-game-mcp ./cmd/stoneage-ai-runner ./cmd/stoneage-ai-worker

# The AI runtime is a separate image target.  It deliberately does not inherit
# the control-plane image: the Codex process must never share the admin
# database, operator socket, Docker socket or funding-policy mounts.
#
# BuildKit executes this stage for the requested target platform, so npm's
# optional dependency resolves the matching official Codex native package for
# linux/amd64 or linux/arm64.
FROM node:22-bookworm-slim AS ai-runtime

ARG TARGETARCH
ARG CODEX_CLI_VERSION=0.154.0

RUN apt-get update \
    && apt-get install --no-install-recommends -y ca-certificates git bash adduser \
    && rm -rf /var/lib/apt/lists/* \
    && case "$TARGETARCH" in amd64|arm64) ;; *) echo "unsupported AI runtime architecture: $TARGETARCH" >&2; exit 2 ;; esac \
    && npm install --global --no-fund --no-audit "@openai/codex@${CODEX_CLI_VERSION}" \
    && test "$(codex --version)" = "codex-cli ${CODEX_CLI_VERSION}" \
    && npm cache clean --force \
    && addgroup --system --gid 10001 stoneage-ai \
    && adduser --system --uid 10001 --ingroup stoneage-ai --home /var/lib/stoneage-ai --shell /usr/sbin/nologin stoneage-ai \
    && install -d -o stoneage-ai -g stoneage-ai -m 0700 /var/lib/stoneage-ai

COPY --from=build-ai /out/stoneage-game-mcp /usr/local/bin/stoneage-game-mcp
COPY --from=build-ai /out/stoneage-ai-runner /usr/local/bin/stoneage-ai-runner
COPY --from=build-ai /out/stoneage-ai-worker /usr/local/bin/stoneage-ai-worker
COPY ai/skills/ /opt/stoneage/ai/skills/

# Fail the image build if the fixed process contract drifts.  The broker owns
# the profile ID, state volume and request input; all executable and Skill
# paths remain image-owned and are never supplied by stdin.
RUN test -x /usr/local/bin/codex \
    && test -x /usr/bin/git \
    && test -x /usr/local/bin/stoneage-game-mcp \
    && test -x /usr/local/bin/stoneage-ai-runner \
    && test -x /usr/local/bin/stoneage-ai-worker \
    && test -d /opt/stoneage/ai/skills

ENV HOME=/var/lib/stoneage-ai \
    STONEAGE_AI_CODEX_BINARY=/usr/local/bin/codex \
    STONEAGE_AI_MCP_BINARY=/usr/local/bin/stoneage-game-mcp \
    STONEAGE_AI_GIT_BINARY=/usr/bin/git \
    STONEAGE_AI_SKILL_ROOT=/opt/stoneage/ai/skills \
    STONEAGE_AI_STATE_ROOT=/var/lib/stoneage-ai \
    STONEAGE_CODEX_CLI_VERSION=${CODEX_CLI_VERSION}

USER stoneage-ai
WORKDIR /var/lib/stoneage-ai

# Execute the actual packaged runner and Codex under the final non-root user
# on both target architectures. The local Responses fixture needs no network
# or credentials; its temporary config and thread state are removed on exit.
RUN --network=none --mount=type=bind,source=scripts/ai-runtime-smoke.cjs,target=/tmp/ai-runtime-smoke.cjs \
    node /tmp/ai-runtime-smoke.cjs /var/lib/stoneage-ai

# The broker supplies exactly one JSON request on stdin.  Keep this fixed so a
# container cannot silently turn into an interactive Codex shell or an
# alternate executable.  The runner materializes a per-profile CODEX_HOME for
# each child process; this image intentionally does not set CODEX_HOME.
ENTRYPOINT ["/usr/local/bin/stoneage-ai-runner"]

FROM alpine:3.22 AS control-plane

# Use local time consistently for server announcements and runtime logs.
ENV TZ=Asia/Shanghai

RUN apk add --no-cache tzdata bash docker-cli docker-cli-compose netcat-openbsd

COPY --from=build-control /out/stoneage-gateway /opt/stoneage/bin/stoneage-gateway
COPY --from=build-control /out/stoneage-admin /opt/stoneage/bin/stoneage-admin
COPY --from=build-control /out/stoneage-operator /opt/stoneage/bin/stoneage-operator
COPY --from=build-control /out/stoneage-assets-sync /opt/stoneage/bin/stoneage-assets-sync
COPY --from=build-control /out/stoneage-web /opt/stoneage/bin/stoneage-web
# The admin and Web processes only verify/read these reviewed native Skills;
# Codex itself runs in the separate ai-runtime image.
COPY ai/skills/ /opt/stoneage/ai/skills/
COPY ai/catalogs/ /opt/stoneage/ai/catalogs/
# Public game assets are distributed separately and mounted by Compose.
COPY deploy/linux/compose/ /opt/stoneage/compose/

RUN chmod 0755 /opt/stoneage/bin/* /opt/stoneage/compose/*.sh \
    && test -d /opt/stoneage/ai/skills \
    && test -n "$(find /opt/stoneage/ai/skills -mindepth 2 -maxdepth 2 -name SKILL.md -type f -print -quit)"

WORKDIR /opt/stoneage
