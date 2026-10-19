Golden responses, checked by the tests: JSON shapes, error codes, status codes, headers on raw files
and cookies. Tokens, ids and times in them are throwaway, from a test run. After changing what the
API answers, rewrite them with `go test -tags nodynamic ./internal/httpapi -update-responses`, and
review the diff before committing it.
