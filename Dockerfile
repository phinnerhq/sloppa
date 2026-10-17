# syntax=docker/dockerfile:1

# Official Node images (https://hub.docker.com/_/node).
ARG NODE_VERSION=26

FROM node:${NODE_VERSION}-slim AS build
# Corepack left Node in v25: pnpm comes from npm, at the version package.json pins.
RUN npm install --global pnpm@12.5.1
WORKDIR /app
# `pnpm fetch` reads the lockfile alone, so the store lands in a layer that reruns only when
# dependencies change, and that CI's layer cache keeps (https://pnpm.io/docker).
COPY pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm fetch
COPY . .
RUN pnpm install --offline --frozen-lockfile --ignore-scripts \
 && pnpm --filter @sloppa/web --filter @sloppa/server build
# The server is one bundled file, and sharp, left out of it for its native libvips, is installed
# beside it at the locked version. Everything ends up readable by the runtime's `node` user.
RUN mkdir /out && cp apps/server/dist/* /out/ && cp -r apps/web/dist /out/public \
 && cd /out && npm install --no-audit --no-fund \
      "sharp@$(node -p "require('/app/apps/server/node_modules/sharp/package.json').version")" \
 && chmod -R a+rX /out

FROM node:${NODE_VERSION}-slim
WORKDIR /app
# Only the data dir is writable by the app; the code stays root-owned.
RUN mkdir /data && chown node:node /data
COPY --from=build /out ./

# Listening on all interfaces is container-local: the app, previews and the CLI's tunnels all on
# 4100. Publish it on 127.0.0.1, or leave it to a reverse proxy on the same network.
ENV NODE_ENV=production \
    HOST=0.0.0.0 \
    PORT=4100 \
    DATA_DIR=/data \
    STATIC_DIR=/app/public

USER node
VOLUME /data
EXPOSE 4100

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD ["node", "-e", "fetch(`http://127.0.0.1:${process.env.PORT}/api/openapi.json`).then((r) => process.exit(r.ok ? 0 : 1), () => process.exit(1))"]

ENTRYPOINT ["node", "--enable-source-maps", "main.mjs"]
