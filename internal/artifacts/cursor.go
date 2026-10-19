package artifacts

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Cursors are opaque to clients: where the last page ended, in its sort order, as base64url JSON. The
// format is the TypeScript server's, so a dashboard open across the upgrade keeps paging.

// artifactCursor is a position in the artifacts, newest first. Fields are pointers so a cursor
// missing one is no cursor at all.
type artifactCursor struct {
	Ms *float64 `json:"ms"`
	ID *string  `json:"id"`
}

func (c artifactCursor) valid() bool { return c.Ms != nil && c.ID != nil }

func artifactCursorAt(ms int64, id string) any {
	return struct {
		Ms int64  `json:"ms"`
		ID string `json:"id"`
	}{ms, id}
}

// groupCursor is a position in the collections, most recently active first.
type groupCursor struct {
	Ms         *float64 `json:"ms"`
	Collection *string  `json:"collection"`
}

func (c groupCursor) valid() bool { return c.Ms != nil && c.Collection != nil }

func groupCursorAt(ms int64, collection string) any {
	return struct {
		Ms         int64  `json:"ms"`
		Collection string `json:"collection"`
	}{ms, collection}
}

func encodeCursor(position any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	// As JSON.stringify writes it.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(position); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
}

// decodeCursor reads a cursor; one that isn't, blank or garbled, starts from the top.
func decodeCursor[C interface{ valid() bool }](cursor string) (C, bool) {
	var c C
	if cursor == "" {
		return c, false
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(cursor, "="))
	if err != nil || json.Unmarshal(data, &c) != nil {
		return c, false
	}
	return c, c.valid()
}
