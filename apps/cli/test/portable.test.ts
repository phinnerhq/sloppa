// What has to hold on every OS, Windows included: a dev command started through a package
// manager's shim, with its arguments intact, served at its URL, and stopped from sloppa with
// everything it started. No signals or /proc here, so it runs the same everywhere.
import assert from "node:assert/strict"
import { after, afterEach, before, test } from "node:test"
import { alive, api, fixture, removeWork, run, startSloppa, stopStarted, waitFor, work } from "./harness.ts"

let sloppa: Awaited<ReturnType<typeof startSloppa>>

before(async () => {
  sloppa = await startSloppa()
})

afterEach(stopStarted)

after(() => {
  sloppa?.process.kill()
  removeWork()
})

test("serves a dev server started through npm, and stops all of it from sloppa", async () => {
  // npm is a .cmd shim on Windows, which Node only runs through cmd.exe. Run from outside this repo,
  // as in an agent's own project: npm would otherwise hold it to this repo's devEngines.
  const args = ["two words", "amp & pipe |", 'quote"d', "%PATH%"]
  const preview = await run(sloppa, [], ["npm", "exec", "--", "node", fixture, ...args], work)
  const [server, grandchild] = preview.pids()

  const body = (await (await fetch(`${preview.url}hello`)).json()) as { path: string; args: Array<string> }
  assert.equal(body.path, "/hello")
  assert.deepEqual(body.args, args)

  assert.equal((await api(sloppa, "DELETE", `/api/previews/${preview.id}`)).status, 204)
  assert.equal(await preview.exited, 0)
  await waitFor(() => !alive(server) && !alive(grandchild), "the dev server and what it started to stop")
  assert.equal((await fetch(preview.url)).status, 404)
})
