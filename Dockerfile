# syntax=docker/dockerfile:1

# The dashboard, built with Node (https://hub.docker.com/_/node).
FROM node:26-slim AS web
RUN npm install --global pnpm@12.5.1
WORKDIR /app
# `pnpm fetch` reads the lockfile alone, so the store lands in a layer that reruns only when
# dependencies change (https://pnpm.io/docker).
COPY pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm fetch
COPY . .
RUN pnpm install --offline --frozen-lockfile --ignore-scripts && pnpm --filter @sloppa/web build

# The server and CLI, one static binary with the dashboard compiled in.
FROM golang:1.26 AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
COPY --from=web /app/apps/web/dist internal/webui/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -tags nodynamic -ldflags "-s -w -X main.version=${VERSION}" -o /sloppa ./cmd/sloppa \
 && mkdir /data

# CA certificates for S3 over TLS, a nonroot user, and nothing else.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /sloppa /sloppa
# Only the data dir is writable by the app; distroless's nonroot user is 65532.
COPY --from=go --chown=65532:65532 /data /data
VOLUME /data

# Listening on all interfaces is container-local: the app, previews and the CLI's tunnels all on
# 4100. Publish it on 127.0.0.1, or leave it to a reverse proxy on the same network.
ENV HOST=0.0.0.0 \
    PORT=4100 \
    DATA_DIR=/data

EXPOSE 4100

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/sloppa", "healthcheck"]

ENTRYPOINT ["/sloppa", "serve"]
