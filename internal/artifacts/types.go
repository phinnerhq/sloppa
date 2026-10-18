// Package artifacts is everything one can do with artifacts: storage behind a SQLite index, content
// types, and thumbnails.
package artifacts

import (
	"errors"
	"io"
)

// ListQuery filters and pages the artifact list.
type ListQuery struct {
	// Collection and Q (a substring of name, id, collection or content type) are ignored when blank.
	Collection, Q string
	Cursor        string
	Limit         int
}

// Upload is one file of a multipart upload, streamed.
type Upload struct {
	Name string
	// ContentType is what the uploader claimed; the file name wins when it's missing or generic.
	ContentType string
	Body        io.Reader
	// Size is -1 when unknown, as for a multipart part.
	Size       int64
	Collection string
}

// ErrNoThumbnail means the artifact can't be shrunk: the route sends the browser to the original.
var ErrNoThumbnail = errors.New("no thumbnail")
