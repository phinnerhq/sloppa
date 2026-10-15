import {
  Api,
  ArtifactId,
  Authorization,
  GROUP_PAGE_SIZE,
  PAGE_SIZE,
  StorageError,
  Unauthorized,
  Uploader
} from "@sloppa/api"
import { Effect, FileSystem, Layer, Option, Redacted, Schema, Stream } from "effect"
import { HttpRouter, HttpServerRequest, HttpServerResponse, HttpStaticServer } from "effect/http"
import { HttpApiBuilder } from "effect/http-api"
import { timingSafeEqual } from "node:crypto"
import { Artifacts } from "./Artifacts.ts"
import { AppConfig } from "./Config.ts"
import { McpRoutes } from "./Mcp.ts"
import { ArtifactStorage, uploadOf } from "./Storage.ts"
import { Thumbnails } from "./Thumbnails.ts"
import { UploadTickets } from "./Tickets.ts"

const sameSecret = (a: string, b: string) => {
  const left = Buffer.from(a)
  const right = Buffer.from(b)
  return left.length === right.length && timingSafeEqual(left, right)
}

const AuthorizationLive = Layer.effect(
  Authorization,
  Effect.gen(function* () {
    const { uploadToken } = yield* AppConfig
    return Authorization.of({
      bearer: (httpEffect, { credential }) =>
        sameSecret(Redacted.value(credential), Redacted.value(uploadToken))
          ? Effect.provideService(httpEffect, Uploader, { token: "valid" })
          : Effect.fail(new Unauthorized({ message: "Missing or invalid token" }))
    })
  })
)

const ArtifactsHandlers = HttpApiBuilder.group(
  Api,
  "artifacts",
  Effect.fn(function* (handlers) {
    const artifacts = yield* Artifacts
    const fs = yield* FileSystem.FileSystem
    /** A multipart temp file, streamed from disk. */
    const uploadOfFile = (path: string) =>
      fs.stat(path).pipe(
        Effect.map((info) => ({
          size: Number(info.size),
          stream: fs
            .stream(path)
            .pipe(Stream.mapError((cause) => new StorageError({ message: `read upload: ${cause.message}` })))
        })),
        Effect.mapError((cause) => new StorageError({ message: `read upload: ${cause.message}` }))
      )
    return handlers.handleAll({
      list: ({ query: { noPr, ...query } }) =>
        artifacts.list({ ...query, noPr: noPr === "true", limit: query.limit ?? PAGE_SIZE }),
      groups: ({ query }) => artifacts.groups({ ...query, limit: query.limit ?? GROUP_PAGE_SIZE }),
      stats: () => artifacts.stats,
      prs: ({ query }) => artifacts.prs(query.repo),
      repos: () => artifacts.repos,
      get: ({ params }) => artifacts.get(params.id),
      upload: ({ payload }) =>
        Effect.forEach(payload.files, (file) =>
          uploadOfFile(file.path).pipe(
            Effect.flatMap((body) =>
              artifacts.upload({
                name: file.name,
                contentType: file.contentType,
                body,
                repo: payload.repo,
                pr: payload.pr
              })
            )
          )
        ),
      delete: ({ params }) => artifacts.remove(params.id)
    })
  })
)

const contentDisposition = (type: "inline" | "attachment", name: string) =>
  `${type}; filename="${name.replace(/[^\x20-\x7e]|"/g, "_")}"; filename*=UTF-8''${encodeURIComponent(name)}`

const notFound = HttpServerResponse.text("Not found", { status: 404 })

/** Artifacts never change once uploaded, and their ids are unguessable: anyone may cache them forever. */
const immutable = {
  "cache-control": "public, max-age=31536000, immutable",
  "x-content-type-options": "nosniff",
  "access-control-allow-origin": "*"
}

/**
 * Raw files, outside the typed API so each response carries the artifact's own
 * content type. Uploaded HTML/SVG gets an opaque, sandboxed origin.
 */
const RawFileRoute = Layer.effectDiscard(
  Effect.gen(function* () {
    const router = yield* HttpRouter.HttpRouter
    const storage = yield* ArtifactStorage
    const decodeId = Schema.decodeUnknownOption(ArtifactId)

    const serve = Effect.gen(function* () {
      const params = yield* HttpRouter.params
      const request = yield* HttpServerRequest.HttpServerRequest
      const id = decodeId(params["id"])
      if (Option.isNone(id)) return notFound

      const download = new URL(request.url, "http://local").searchParams.has("download")
      return yield* storage.read(id.value, request.headers["range"]).pipe(
        Effect.map(({ artifact, body, contentLength, contentRange }) =>
          HttpServerResponse.stream(body, {
            status: contentRange === undefined ? 200 : 206,
            contentType: artifact.contentType,
            contentLength,
            headers: {
              ...(contentRange === undefined ? {} : { "content-range": contentRange }),
              "accept-ranges": "bytes",
              "content-disposition": contentDisposition(download ? "attachment" : "inline", artifact.name),
              "content-security-policy": "sandbox allow-scripts allow-popups allow-forms allow-downloads",
              ...immutable
            }
          })
        ),
        Effect.catchTags({
          ArtifactNotFound: () => Effect.succeed(notFound),
          StorageError: (e) => Effect.succeed(HttpServerResponse.text(e.message, { status: 502 }))
        })
      )
    })

    yield* router.add("GET", "/f/:id", serve)
    yield* router.add("GET", "/f/:id/:name", serve)
  })
)

/** Previews for the inventory grid; an image that can't be shrunk sends the browser to the original. */
const ThumbnailRoute = Layer.effectDiscard(
  Effect.gen(function* () {
    const router = yield* HttpRouter.HttpRouter
    const thumbnails = yield* Thumbnails
    const decodeId = Schema.decodeUnknownOption(ArtifactId)

    yield* router.add(
      "GET",
      "/t/:id",
      Effect.gen(function* () {
        const id = decodeId((yield* HttpRouter.params)["id"])
        if (Option.isNone(id)) return notFound
        return yield* thumbnails.get(id.value).pipe(
          Effect.map((bytes) =>
            HttpServerResponse.uint8Array(bytes, { contentType: "image/webp", headers: immutable })
          ),
          Effect.catchTags({
            ArtifactNotFound: () => Effect.succeed(notFound),
            StorageError: (e) => Effect.succeed(HttpServerResponse.text(e.message, { status: 502 })),
            ThumbnailError: (e) =>
              Effect.logWarning(`No thumbnail for ${id.value}`, e.cause).pipe(
                Effect.as(HttpServerResponse.redirect(`/f/${id.value}`, { status: 302 }))
              )
          })
        )
      })
    )
  })
)

/**
 * `curl -T file <url>` target for upload tickets handed out by the MCP server.
 * The signed ticket is the credential, so this route takes no bearer token.
 */
const TicketUploadRoute = Layer.effectDiscard(
  Effect.gen(function* () {
    const router = yield* HttpRouter.HttpRouter
    const tickets = yield* UploadTickets
    const artifacts = yield* Artifacts

    yield* router.add(
      "PUT",
      // A wildcard: signed tickets outgrow the router's 100 character limit on named params.
      "/api/uploads/*",
      Effect.gen(function* () {
        const params = yield* HttpRouter.params
        const request = yield* HttpServerRequest.HttpServerRequest
        const ticket = yield* tickets.verify(params["*"] ?? "")
        if (Option.isNone(ticket)) {
          return HttpServerResponse.text("Invalid or expired upload URL, ask for a new one\n", { status: 403 })
        }
        if (yield* artifacts.exists(ticket.value.id)) {
          return HttpServerResponse.text("This upload URL was already used, ask for a new one\n", { status: 409 })
        }
        const body = uploadOf(new Uint8Array(yield* request.arrayBuffer))
        const artifact = yield* artifacts.upload({
          id: ticket.value.id,
          name: ticket.value.name,
          contentType: ticket.value.contentType ?? request.headers["content-type"],
          body,
          repo: ticket.value.repo,
          pr: ticket.value.pr
        })
        return yield* HttpServerResponse.json({ id: artifact.id, url: artifact.url, rawUrl: artifact.rawUrl })
      }).pipe(
        Effect.catchTags({
          StorageError: (e) => Effect.succeed(HttpServerResponse.text(e.message, { status: 502 })),
          HttpServerError: () => Effect.succeed(HttpServerResponse.text("Bad upload body\n", { status: 400 })),
          HttpBodyError: () => Effect.succeed(HttpServerResponse.text("Bad response\n", { status: 500 }))
        })
      )
    )
  })
)

/** The MCP endpoint lives outside the typed API, so it gets the same bearer check here. */
const McpAuthorization = HttpRouter.use(
  Effect.fn(function* (router) {
    const { uploadToken } = yield* AppConfig
    const expected = `Bearer ${Redacted.value(uploadToken)}`
    yield* router.addGlobalMiddleware((httpApp) =>
      Effect.gen(function* () {
        const request = yield* HttpServerRequest.HttpServerRequest
        const path = new URL(request.url, "http://local").pathname
        if (path !== "/mcp" && !path.startsWith("/mcp/")) return yield* httpApp
        if (sameSecret(request.headers["authorization"] ?? "", expected)) return yield* httpApp
        return HttpServerResponse.text("Missing or invalid token\n", {
          status: 401,
          headers: { "www-authenticate": "Bearer" }
        })
      })
    )
  })
)

const StaticFiles = Layer.unwrap(
  Effect.gen(function* () {
    const { staticDir } = yield* AppConfig
    return staticDir === undefined ? Layer.empty : HttpStaticServer.layer({ root: staticDir, spa: true })
  })
)

export const Routes = Layer.mergeAll(
  HttpApiBuilder.layer(Api, { openapiPath: "/api/openapi.json" }).pipe(
    Layer.provide(ArtifactsHandlers.pipe(Layer.provide(AuthorizationLive)))
  ),
  RawFileRoute,
  ThumbnailRoute,
  TicketUploadRoute,
  McpRoutes,
  McpAuthorization,
  StaticFiles
).pipe(Layer.provide(Layer.mergeAll(Artifacts.layer, UploadTickets.layer)))
