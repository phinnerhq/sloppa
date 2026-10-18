Responses from the TypeScript server (master at f15d071), captured with curl, for the Go server to
match where the dashboard depends on them: JSON shapes, error `_tag`s, status codes, headers on raw
files and cookies. Tokens and ids in them are throwaway, from a local run.

Known, deliberate differences: preview start no longer returns a `ticket`/`connectUrl` (see
`api.StartedPreview`), and input that fails validation is still a 400 but now carries a JSON body.
