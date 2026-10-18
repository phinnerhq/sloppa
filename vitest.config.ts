import { defineConfig } from "vitest/config"

export default defineConfig({
  test: {
    include: ["apps/*/test/**/*.test.ts", "packages/*/test/**/*.test.ts"],
    setupFiles: ["vitest.setup.ts"],
    // On GitHub: annotations on the failing lines, and a JUnit report for the test results check.
    reporters: process.env.GITHUB_ACTIONS ? ["default", "github-actions", "junit"] : ["default"],
    outputFile: { junit: "test-results/junit.xml" },
    // Node imports the tests and strips their types itself: so V8 coverage lines up with the .ts
    // sources for fallow, with no transform or source map in between.
    experimental: { viteModuleRunner: false }
  }
})
