import type { CreateClientConfig } from "./client/client.gen.ts"

/** Read by the generated client as it's created (see openapi-ts.config.ts). */
export const createClientConfig: CreateClientConfig = (config) => ({
  ...config,
  // Signed in through an HttpOnly cookie, which same-origin fetches send by themselves.
  baseUrl: window.location.origin,
  credentials: "same-origin"
})
