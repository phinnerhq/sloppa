import { defineConfig } from "vitest/config"

export default defineConfig({
  test: {
    include: ["apps/*/test/**/*.test.ts", "packages/*/test/**/*.test.ts"],
    // One file at a time: the end-to-end suites each start servers, CLIs and dev servers, and
    // assert on timing (an SSE event within a second) that a neighbour's load could break.
    fileParallelism: false,
    // The end-to-end tests run real processes for seconds each, the server's start included.
    testTimeout: 60_000,
    hookTimeout: 60_000,
    setupFiles: ["vitest.setup.ts"],
    // On GitHub: annotations on the failing lines, and a JUnit report for the test results check.
    reporters: process.env.GITHUB_ACTIONS ? ["default", "github-actions", "junit"] : ["default"],
    outputFile: { junit: "test-results/junit.xml" },
    // Node imports the tests and strips their types itself, as it runs the apps: so V8 coverage
    // lines up with the .ts sources for fallow, with no transform or source map in between.
    experimental: { viteModuleRunner: false }
  }
})
