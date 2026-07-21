# syntax=docker/dockerfile:1.7
FROM node:24.18.0-bookworm-slim AS web-build
WORKDIR /workspace
ENV PNPM_HOME=/pnpm
ENV PATH=$PNPM_HOME:$PATH
RUN corepack enable && corepack prepare pnpm@11.9.0 --activate
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY web/package.json web/package.json
RUN --mount=type=cache,target=/pnpm/store pnpm install --frozen-lockfile
COPY web web
RUN pnpm build

FROM golang:1.26.5-bookworm AS server-build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd cmd
COPY db db
COPY internal internal
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/happylearn ./cmd/server \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/happylearn-admin ./cmd/admin

FROM debian:12.12-slim AS runtime
RUN apt-get update && apt-get install --no-install-recommends -y ca-certificates curl && rm -rf /var/lib/apt/lists/*
RUN groupadd --gid 10001 happylearn && useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin happylearn
WORKDIR /app
COPY --from=server-build /out/happylearn /app/happylearn
COPY --from=server-build /out/happylearn-admin /app/happylearn-admin
COPY --from=web-build /workspace/web/dist /app/web/dist
RUN chown -R 10001:10001 /app && chmod -R a=rX /app
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --retries=12 CMD ["curl", "--fail", "--silent", "http://127.0.0.1:8080/api/v1/health/ready"]
ENTRYPOINT ["/app/happylearn"]
