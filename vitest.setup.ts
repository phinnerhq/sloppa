// vitest stops its fork workers with SIGTERM, and Node writes NODE_V8_COVERAGE only on a normal
// exit: exiting on it keeps the workers' coverage, for `fallow health --coverage`.
if (process.env["NODE_V8_COVERAGE"]) process.once("SIGTERM", () => process.exit())
