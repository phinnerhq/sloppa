import {
  Agent,
  Api,
  ArtifactId,
  Authorization,
  CreatedAgent,
  CurrentUser,
  GROUP_PAGE_SIZE,
  PAGE_SIZE,
  SESSION_COOKIE,
  sessionCookie,
  SessionAuthorization,
  SetupLink,
  setupPath,
  StorageError,
  Unauthorized,
  User
} from "@sloppa/api"
import { DateTime, Effect, FileSystem, Layer, Option, Redacted, Schema, Stream } from "effect"
import { HttpMiddleware, HttpRouter, HttpServerRequest, HttpServerResponse, HttpStaticServer } from "effect/http"
import { HttpApiBuilder } from "effect/http-api"
import { Accounts } from "./Accounts.ts"
import { Artifacts } from "./Artifacts.ts"
import { AppConfig } from "./Config.ts"
import { McpRoutes } from "./Mcp.ts"
import { Previews } from "./Previews.ts"
import { SetupRoute, setupPrompt } from "./Setup.ts"
import { ArtifactStorage } from "./Storage.ts"
import { uploadOf } from "./StorageShared.ts"
import { Thumbnails } from "./Thumbnails.ts"
import { UploadTickets } from "./Tickets.ts"

/** Who holds a credential, or Unauthorized with `message`. */
const holderOf =
  (accounts: Accounts["Service"]) =>
  (kind: "session" | "agent", credential: Redacted.Redacted<string>, message: string) =>
    (Redacted.value(credential) === ""
      ? Effect.succeedNone
      : accounts.authenticate(kind, Redacted.value(credential))
    ).pipe(
      Effect.flatMap(
        Option.match({
          onNone: () => Effect.fail(new Unauthorized({ message })),
          onSome: Effect.succeed
        })
      )
    )

const AuthorizationLive = Layer.effect(
  Authorization,
  Effect.gen(function* () {
    const holder = holderOf(yield* Accounts)
    const message = "Sign in, or pass an agent token"
    return Authorization.of({
      session: (httpEffect, { credential }) =>
        Effect.flatMap(holder("session", credential, message), (user) =>
          Effect.provideService(httpEffect, CurrentUser, user)
        ),
      bearer: (httpEffect, { credential }) =>
        Effect.flatMap(holder("agent", credential, message), (user) =>
          Effect.provideService(httpEffect, CurrentUser, user)
        )
    })
  })
)

const SessionAuthorizationLive = Layer.effect(
  SessionAuthorization,
  Effect.gen(function* () {
    const holder = holderOf(yield* Accounts)
    return SessionAuthorization.of({
      session: (httpEffect, { credential }) =>
        Effect.flatMap(holder("session", credential, "Sign in from a browser for this"), (user) =>
          Effect.provideService(httpEffect, CurrentUser, user)
        )
    })
  })
)

const SessionHandlers = HttpApiBuilder.group(
  Api,
  "session",
  Effect.fn(function* (handlers) {
    const accounts = yield* Accounts
    const { publicUrl, trustProxy } = yield* AppConfig
    const clientOf = (request: HttpServerRequest.HttpServerRequest) => {
      const forwarded = trustProxy ? request.headers["x-forwarded-for"]?.split(",").at(-1)?.trim() : undefined
      return forwarded || Option.getOrUndefined(request.remoteAddress)
    }
    // Lax keeps the session on links from elsewhere (a PR), but not on another site's POSTs.
    const cookie = { secure: publicUrl.startsWith("https:"), sameSite: "lax", path: "/" } as const
    return handlers.handleAll({
      login: ({ payload, request }) =>
        accounts.login(payload.username, payload.password, clientOf(request)).pipe(
          Effect.flatMap(({ account, session, expiresAt }) =>
            HttpApiBuilder.securitySetCookie(sessionCookie, session, {
              ...cookie,
              expires: DateTime.toDate(expiresAt)
            }).pipe(Effect.as(new User({ username: account.username })))
          )
        ),
      current: () => Effect.map(CurrentUser, (user) => new User({ username: user.username })),
      changePassword: ({ payload, request }) =>
        Effect.flatMap(CurrentUser, (user) =>
          accounts.changePassword(user, request.cookies[SESSION_COOKIE] ?? "", payload.current, payload.next)
        ),
      logout: () =>
        Effect.gen(function* () {
          const session = (yield* HttpServerRequest.HttpServerRequest).cookies[SESSION_COOKIE]
          if (session !== undefined) yield* accounts.logout(session)
          yield* HttpApiBuilder.securitySetCookie(sessionCookie, "", { ...cookie, maxAge: 0 })
        })
    })
  })
)

const AgentsHandlers = HttpApiBuilder.group(
  Api,
  "agents",
  Effect.fn(function* (handlers) {
    const accounts = yield* Accounts
    const { publicUrl } = yield* AppConfig
    return handlers.handleAll({
      list: () =>
        CurrentUser.pipe(
          Effect.flatMap(accounts.agents),
          Effect.map((agents) =>
            agents.map(
              ({ lastUsedAt, ...agent }) => new Agent({ ...agent, ...(lastUsedAt === undefined ? {} : { lastUsedAt }) })
            )
          )
        ),
      create: ({ payload }) =>
        CurrentUser.pipe(
          Effect.flatMap((user) => accounts.createAgent(user, payload.name)),
          Effect.map(
            ({ agent, token }) =>
              new CreatedAgent({
                agent: new Agent({ id: agent.id, name: agent.name, createdAt: agent.createdAt }),
                token
              })
          )
        ),
      revoke: ({ params }) => Effect.flatMap(CurrentUser, (user) => accounts.revokeAgent(user, params.id)),
      rename: ({ params, payload }) =>
        Effect.flatMap(CurrentUser, (user) => accounts.renameAgent(user, params.id, payload.name)),
      createSetup: () =>
        CurrentUser.pipe(
          Effect.flatMap(accounts.createSetup),
          Effect.map(({ id, code, expiresAt }) => {
            const url = `${publicUrl}${setupPath(code)}`
            return new SetupLink({ id, url, prompt: setupPrompt(publicUrl, url), expiresAt })
          })
        ),
      revokeSetup: ({ params }) => Effect.flatMap(CurrentUser, (user) => accounts.revokeSetupById(user, params.id))
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
      list: ({ query }) => artifacts.list({ ...query, limit: query.limit ?? PAGE_SIZE }),
      mosaic: ({ query }) => artifacts.mosaic({ ...query, limit: query.limit ?? GROUP_PAGE_SIZE }),
      stats: () => artifacts.stats,
      collections: () => artifacts.collections,
      get: ({ params }) => artifacts.get(params.id),
      upload: ({ payload }) =>
        Effect.forEach(payload.files, (file) =>
          uploadOfFile(file.path).pipe(
            Effect.flatMap((body) =>
              artifacts.upload({
                name: file.name,
                contentType: file.contentType,
                body,
                collection: payload.collection
              })
            )
          )
        ),
      move: ({ params, payload }) => artifacts.move(params.id, payload.collection),
      delete: ({ params }) => artifacts.remove(params.id)
    })
  })
)

const PreviewsHandlers = HttpApiBuilder.group(
  Api,
  "previews",
  Effect.fn(function* (handlers) {
    const previews = yield* Previews
    return handlers.handleAll({
      list: () => Effect.flatMap(CurrentUser, previews.list),
      start: ({ payload }) => Effect.flatMap(CurrentUser, (user) => previews.start(user, payload)),
      stop: ({ params }) => Effect.flatMap(CurrentUser, (user) => previews.stop(user, params.id))
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
 * The request log, naming `path` instead of the real one: these routes carry a secret in theirs.
 * Provided to a route's layer, it applies to that route alone.
 */
const logAs = (path: string) =>
  HttpRouter.middleware((httpApp) =>
    Effect.gen(function* () {
      const request = yield* HttpServerRequest.HttpServerRequest
      const response = yield* HttpMiddleware.withLoggerDisabled(httpApp)
      yield* Effect.log("Sent HTTP response").pipe(
        Effect.annotateLogs({ "http.method": request.method, "http.url": path, "http.status": response.status })
      )
      return response
    })
  ).layer

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
        // The claim covers this process; the existence check, a ticket used before a restart.
        if (!(yield* tickets.claim(ticket.value)) || (yield* artifacts.exists(ticket.value.id))) {
          return HttpServerResponse.text("This upload URL was already used, ask for a new one\n", { status: 409 })
        }
        const artifact = yield* Effect.gen(function* () {
          const body = uploadOf(new Uint8Array(yield* request.arrayBuffer))
          return yield* artifacts.upload({
            id: ticket.value.id,
            name: ticket.value.name,
            contentType: ticket.value.contentType ?? request.headers["content-type"],
            body,
            collection: ticket.value.collection
          })
        }).pipe(Effect.onError(() => tickets.release(ticket.value)))
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

const StaticFiles = Layer.unwrap(
  Effect.gen(function* () {
    const { staticDir } = yield* AppConfig
    return staticDir === undefined ? Layer.empty : HttpStaticServer.layer({ root: staticDir, spa: true })
  })
)

export const Routes = Layer.mergeAll(
  HttpApiBuilder.layer(Api, { openapiPath: "/api/openapi.json" }).pipe(
    Layer.provide(Layer.mergeAll(ArtifactsHandlers, SessionHandlers, AgentsHandlers, PreviewsHandlers)),
    Layer.provide(Layer.mergeAll(AuthorizationLive, SessionAuthorizationLive))
  ),
  RawFileRoute,
  ThumbnailRoute,
  TicketUploadRoute.pipe(Layer.provide(logAs("/api/uploads/<ticket>"))),
  McpRoutes,
  SetupRoute.pipe(Layer.provide(logAs("/setup/<code>"))),
  StaticFiles
).pipe(
  Layer.provide(Layer.mergeAll(Artifacts.layer, UploadTickets.layer, Previews.layer)),
  Layer.provideMerge(Accounts.layer)
)
