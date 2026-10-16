# syntax=docker/dockerfile:1

# Official Node images (https://hub.docker.com/_/node).
ARG NODE_VERSION=26

FROM node:${NODE_VERSION}-slim AS base
# Corepack left Node in v25: pnpm comes from npm, pinned to the version in package.json.
RUN npm install --global pnpm@12.5.1
WORKDIR /app
# Manifests first: the install layers only rerun when dependencies change, not on every source edit.
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY packages/api/package.json packages/api/
COPY packages/preview/package.json packages/preview/
COPY apps/server/package.json apps/server/
COPY apps/web/package.json apps/web/
COPY apps/cli/package.json apps/cli/

FROM base AS web
RUN --mount=type=cache,id=pnpm,target=/pnpm-store \
    pnpm install --frozen-lockfile --store-dir /pnpm-store --filter @sloppa/web... --ignore-scripts
COPY packages/api packages/api
COPY apps/web apps/web
RUN pnpm --filter @sloppa/web build

# The server, bundled into one file like the CLI: @distilled.cloud/aws ships all 439 AWS services
# (200 MB of source, types and maps) and only S3 is used, which bundling keeps to a fraction of that.
FROM base AS server
RUN --mount=type=cache,id=pnpm,target=/pnpm-store \
    pnpm install --frozen-lockfile --store-dir /pnpm-store --filter @sloppa/server... --ignore-scripts
COPY packages/api/src packages/api/src
COPY packages/preview/src packages/preview/src
COPY apps/server apps/server
RUN pnpm --filter @sloppa/server build
# sharp stays out of the bundle for its native libvips: it's installed beside it, at the locked version.
# Then everything is made readable by the runtime's `node` user, whatever the build context's umask.
RUN mkdir /out && cp apps/server/dist/main.mjs apps/server/dist/main.mjs.map /out/ \
 && cd /out && echo '{"type":"module","private":true}' > package.json \
 && npm install --omit=dev --no-audit --no-fund \
      "sharp@$(node -p "require('/app/apps/server/node_modules/sharp/package.json').version")" \
 && chmod -R a+rX /out

FROM node:${NODE_VERSION}-slim AS runtime

WORKDIR /app

# Only the data dir is writable by the app; the code stays root-owned.
RUN mkdir /data && chown node:node /data

COPY --from=server /out ./
COPY --from=web /app/apps/web/dist ./public

# Listening on all interfaces is container-local: the app on 4100, previews and the CLI's tunnels
# on 4200. Publish them on 127.0.0.1, or leave them to a reverse proxy on the same network.
ENV NODE_ENV=production \
    HOST=0.0.0.0 \
    PORT=4100 \
    PREVIEW_PORT=4200 \
    DATA_DIR=/data \
    STATIC_DIR=/app/public

USER node

VOLUME /data
EXPOSE 4100 4200

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD ["node", "-e", "fetch(`http://127.0.0.1:${process.env.PORT}/api/openapi.json`).then((r) => process.exit(r.ok ? 0 : 1), () => process.exit(1))"]

ENTRYPOINT ["node", "--enable-source-maps", "main.mjs"]
