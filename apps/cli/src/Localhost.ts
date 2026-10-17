import { lookup } from "node:dns"
import type { LookupFunction } from "node:net"

/**
 * Name lookups where `localhost` and every name under it are loopback (RFC 6761), as browsers have
 * them: a local sloppa's previews live at `<name>.preview.localhost`, and Windows only resolves
 * `localhost` itself. Anything else goes to the system's resolver.
 */
export const lookupLocalhost: LookupFunction = (hostname, options, callback) => {
  if (hostname !== "localhost" && !hostname.endsWith(".localhost")) return lookup(hostname, options, callback)
  const loopback = [
    { address: "127.0.0.1", family: 4 },
    { address: "::1", family: 6 }
  ].filter(({ family }) => options.family === undefined || options.family === 0 || options.family === family)
  if (options.all) return callback(null, loopback)
  const [first = { address: "127.0.0.1", family: 4 }] = loopback
  callback(null, first.address, first.family)
}
