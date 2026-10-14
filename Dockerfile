# syntax=docker/dockerfile:1

# Official Bun images (https://hub.docker.com/r/oven/bun), pinned to the version the lockfile was made with.
ARG BUN_VERSION=1.4.3

FROM oven/bun:${BUN_VERSION} AS build

# Keep Bun's package cache in a BuildKit cache mount, so dependency changes only fetch what's new.
ENV BUN_INSTALL_CACHE_DIR=/bun-cache

WORKDIR /app

# Manifests first: the install layer only reruns when dependencies change, not on every source edit.
COPY package.json bun.lock ./
COPY packages/api/package.json packages/api/
COPY apps/server/package.json apps/server/
COPY apps/web/package.json apps/web/

RUN --mount=type=cache,target=/bun-cache \
    bun install --frozen-lockfile

COPY . .

# The SPA, and the server bundled into a single file: the runtime image needs no node_modules.
RUN bun run build

FROM oven/bun:${BUN_VERSION}-slim AS runtime

WORKDIR /app

# Only the data dir is writable by the app; the code stays root-owned.
RUN mkdir /data && chown bun:bun /data

COPY --from=build /app/apps/server/dist ./
COPY --from=build /app/apps/web/dist ./public

# Listening on all interfaces is container-local; publish the port on 127.0.0.1 or behind a proxy.
ENV NODE_ENV=production \
    HOST=0.0.0.0 \
    PORT=4100 \
    DATA_DIR=/data \
    STATIC_DIR=/app/public

USER bun

VOLUME /data
EXPOSE 4100

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD ["bun", "-e", "fetch(`http://127.0.0.1:${process.env.PORT}/api/openapi.json`).then((r) => process.exit(r.ok ? 0 : 1), () => process.exit(1))"]

ENTRYPOINT ["bun", "main.js"]
