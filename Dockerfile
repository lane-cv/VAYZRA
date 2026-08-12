# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
FROM node:24.18.0-bookworm-slim@sha256:6f7b03f7c2c8e2e784dcf9295400527b9b1270fd37b7e9a7285cf83b6951452d AS web-build
WORKDIR /workspace
ENV PNPM_HOME=/pnpm
ENV PATH=$PNPM_HOME:$PATH
RUN corepack enable && corepack prepare pnpm@11.9.0 --activate
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY web/package.json web/package.json
RUN --mount=type=cache,target=/pnpm/store pnpm install --frozen-lockfile
COPY web web
RUN pnpm build

FROM golang:1.26.5-bookworm@sha256:53eeac89074db483fdf0ab3be1df32bf6e47562263d2d0d6baa7f26acb4957dd AS go-build-base
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd cmd
COPY db db
COPY internal internal

FROM go-build-base AS server-build
ARG HAPPYLEARN_BUILD_VERSION
ARG HAPPYLEARN_BUILD_COMMIT
ARG HAPPYLEARN_BUILD_TIME
ARG HAPPYLEARN_BUILD_MIN_SCHEMA
ARG HAPPYLEARN_BUILD_MAX_SCHEMA
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.buildVersion=${HAPPYLEARN_BUILD_VERSION} -X main.buildCommit=${HAPPYLEARN_BUILD_COMMIT} -X main.buildTime=${HAPPYLEARN_BUILD_TIME} -X main.buildMinSchema=${HAPPYLEARN_BUILD_MIN_SCHEMA} -X main.buildMaxSchema=${HAPPYLEARN_BUILD_MAX_SCHEMA}" \
      -o /out/happylearn ./cmd/server \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/happylearn-admin ./cmd/admin

FROM go-build-base AS migrate-build
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/happylearn-migrate ./cmd/migrate \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/happylearn-release-control ./cmd/release-control

FROM go-build-base AS acceptance-build
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/happylearn-acceptance ./cmd/acceptance

FROM go-build-base AS release-manifest-build
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/happylearn-release-manifest ./cmd/release-manifest

FROM debian:12.12-slim@sha256:d5d3f9c23164ea16f31852f95bd5959aad1c5e854332fe00f7b3a20fcc9f635c AS runtime-base
RUN apt-get update && apt-get install --no-install-recommends -y ca-certificates curl && rm -rf /var/lib/apt/lists/*
RUN groupadd --gid 10001 happylearn && useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin happylearn
WORKDIR /app

FROM runtime-base AS migrate
COPY --from=migrate-build --chown=10001:10001 /out/happylearn-migrate /app/happylearn-migrate
COPY --from=migrate-build --chown=10001:10001 /out/happylearn-release-control /app/happylearn-release-control
RUN chmod 0555 /app/happylearn-migrate /app/happylearn-release-control
USER 10001:10001
ENTRYPOINT ["/app/happylearn-migrate"]

FROM runtime-base AS acceptance
COPY --from=acceptance-build --chown=10001:10001 /out/happylearn-acceptance /app/happylearn-acceptance
RUN chmod 0555 /app/happylearn-acceptance
USER 10001:10001
ENTRYPOINT ["/app/happylearn-acceptance"]

FROM runtime-base AS server
COPY --from=server-build /out/happylearn /app/happylearn
COPY --from=server-build /out/happylearn-admin /app/happylearn-admin
COPY --from=acceptance-build /out/happylearn-acceptance /app/happylearn-acceptance
COPY --from=release-manifest-build /out/happylearn-release-manifest /app/happylearn-release-manifest
COPY --from=web-build /workspace/web/dist /app/web/dist
RUN chown -R 10001:10001 /app && chmod -R a=rX /app
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --retries=12 CMD ["curl", "--fail", "--silent", "http://127.0.0.1:8080/api/v1/health/ready"]
ENTRYPOINT ["/app/happylearn"]

FROM server AS runtime
