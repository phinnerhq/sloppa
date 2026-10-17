// What has to hold on every OS, Windows included: a dev command started through a package
// manager's shim, with its arguments intact, served at its URL, and stopped from sloppa with
// everything it started. No signals or /proc here, so it runs the same everywhere.
import { afterAll, afterEach, beforeAll, expect, it } from "vitest"
import {
  alive,
  api,
  fixture,
  get,
  removeWork,
  run,
  startSloppa,
  status,
  stopServers,
  stopStarted,
  waitFor,
  work
} from "./harness.ts"

let sloppa: Awaited<ReturnType<typeof startSloppa>>

beforeAll(async () => {
  sloppa = await startSloppa()
})

afterEach(stopStarted)

afterAll(async () => {
  await stopServers()
  removeWork()
})

it("serves a dev server started through npm, and stops all of it from sloppa", async () => {
  // npm is a .cmd shim on Windows, which Node only runs through cmd.exe. Run from outside this repo,
  // as in an agent's own project: npm would otherwise hold it to this repo's devEngines. Node by its
  // path, since npm exec looks for a bare `node` without the .exe and would fetch the npm package
  // of that name instead; offline, so it fails rather than tries.
  const args = ["two words", "amp & pipe |", 'quote"d', "%PATH%"]
  const preview = await run(sloppa, [], ["npm", "exec", "--offline", "--", process.execPath, fixture, ...args], work)
  const [server, grandchild] = preview.pids()

  // Not fetch: Windows can't resolve the preview's name under preview.localhost by itself.
  const body = JSON.parse((await get(`${preview.url}hello`)).body.toString()) as { path: string; args: Array<string> }
  expect(body.path).toBe("/hello")
  expect(body.args).toEqual(args)

  expect((await api(sloppa, "DELETE", `/api/previews/${preview.id}`)).status).toBe(204)
  expect(await preview.exited, preview.stderr()).toBe(0)
  await waitFor(() => !alive(server) && !alive(grandchild), "the dev server and what it started to stop")
  expect(await status(preview.url)).toBe(404)
})
