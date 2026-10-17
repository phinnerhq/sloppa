import dns from "node:dns"
import { lookupLocalhost } from "./apps/cli/src/Localhost.ts"

// vitest stops its fork workers with SIGTERM, and Node writes NODE_V8_COVERAGE only on a normal
// exit: exiting on it keeps the workers' coverage, for `fallow health --coverage`.
if (process.env["NODE_V8_COVERAGE"]) process.once("SIGTERM", () => process.exit())

// The tests' previews live under preview.localhost, which Windows never resolves and macOS not
// always: fetch and ws reach them the way the CLI does.
dns.lookup = lookupLocalhost as typeof dns.lookup
