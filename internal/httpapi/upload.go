package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/artifacts"
)

// uploadOverhead is room around the files of an upload, for multipart framing and the collection.
// Like the TypeScript server, a whole upload is bounded by one file's limit plus this.
const uploadOverhead = 10 << 20

var errFileTooLarge = errors.New("file too large")

// registerUpload serves POST /api/artifacts, streaming each file of the multipart body to storage
// as it arrives rather than buffering it as Huma would. The CLI sends the collection field first,
// which this needs, since a file can't be stored before it's known where to file it.
func (s *server) registerUpload(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/artifacts", s.withAuth(signedIn, s.upload))

	registry := s.api.OpenAPI().Components.Schemas
	s.api.OpenAPI().AddOperation(&huma.Operation{
		OperationID: "artifacts.upload",
		Method:      http.MethodPost,
		Path:        "/api/artifacts",
		Tags:        []string{"Artifacts"},
		Description: "Uploads files into a collection. The collection field must come before the files, and each file " +
			"can be at most 100 MiB. When one file fails, none are kept.",
		Security: signedIn.security(),
		RequestBody: &huma.RequestBody{
			Required: true,
			Content: map[string]*huma.MediaType{
				"multipart/form-data": {
					Schema: &huma.Schema{
						Type: "object",
						Properties: map[string]*huma.Schema{
							"collection": {
								Type:        "string",
								MinLength:   new(1),
								MaxLength:   new(api.CollectionMax),
								Description: `Filed under this collection, e.g. "owner/repo #42".`,
							},
							"files": {Type: "array", Items: &huma.Schema{Type: "string", Format: "binary"}},
						},
						Required: []string{"collection", "files"},
					},
				},
			},
		},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "OK",
				Content: map[string]*huma.MediaType{
					"application/json": {Schema: registry.Schema(reflect.TypeFor[[]api.Artifact](), true, "")},
				},
			},
		},
		Metadata: map[string]any{errorsKey: []api.ErrorKind{api.KindStorageError}},
	})
}

func (s *server) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, api.MaxUploadBytes+uploadOverhead)
	parts, err := r.MultipartReader()
	if err != nil {
		writeError(w, r, api.BadRequest("Expected a multipart/form-data body"))
		return
	}

	var (
		collection string
		uploaded   []api.Artifact
	)
	fail := func(err error) {
		s.forget(r.Context(), uploaded)
		writeError(w, r, err)
	}
	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fail(requestBodyError(err))
			return
		}
		// Every part with a file name is a file; of the other fields only the collection matters.
		if part.FileName() == "" {
			if part.FormName() != "collection" {
				continue
			}
			if collection != "" {
				fail(api.BadRequest("body.collection: expected one collection, got several"))
				return
			}
			// Room for CollectionMax characters of 4 bytes each, and space around them.
			raw, err := io.ReadAll(io.LimitReader(part, 4*api.CollectionMax+1024))
			if err != nil {
				fail(requestBodyError(err))
				return
			}
			if collection, err = trimmedName("collection", string(raw), api.CollectionMax); err != nil {
				fail(err)
				return
			}
			continue
		}
		if collection == "" {
			fail(api.BadRequest("body.collection: expected the collection field before the files"))
			return
		}

		body := &cappedReader{r: part, left: api.MaxUploadBytes}
		a, err := s.artifacts.Upload(r.Context(), artifacts.Upload{
			Name:        part.FileName(),
			ContentType: part.Header.Get("Content-Type"),
			Body:        body,
			Size:        -1,
			Collection:  collection,
		})
		// A failure reading the request is the client's, however storage reported it.
		if body.err != nil {
			fail(requestBodyError(body.err))
			return
		}
		if err != nil {
			fail(err)
			return
		}
		uploaded = append(uploaded, a)
	}
	if collection == "" {
		writeError(w, r, api.BadRequest("body.collection: expected a collection field"))
		return
	}
	if len(uploaded) == 0 {
		writeError(w, r, api.BadRequest("body.files: expected at least one file"))
		return
	}
	writeJSON(w, http.StatusOK, uploaded)
}

// forget removes what a failed upload stored before failing, so it fails as a whole.
func (s *server) forget(ctx context.Context, uploaded []api.Artifact) {
	ctx = context.WithoutCancel(ctx)
	for _, a := range uploaded {
		if err := s.artifacts.Remove(ctx, a.ID); err != nil {
			slog.WarnContext(ctx, "Couldn't remove an artifact of a failed upload", "id", a.ID, "error", err)
		}
	}
}

func requestBodyError(err error) error {
	var tooBig *http.MaxBytesError
	switch {
	case errors.Is(err, errFileTooLarge):
		return api.PayloadTooLarge(fmt.Sprintf("Each file can be at most %d MiB", api.MaxUploadBytes>>20))
	case errors.As(err, &tooBig):
		return api.PayloadTooLarge(fmt.Sprintf("An upload can be at most %d MiB in all", tooBig.Limit>>20))
	}
	return api.BadRequest("Couldn't read the multipart body: " + err.Error())
}

// cappedReader fails past left bytes, and keeps the error reading r, if any.
type cappedReader struct {
	r    io.Reader
	left int64
	err  error
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	// One byte more than allowed tells a file that's exactly at the limit from one past it.
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	if int64(n) > c.left {
		c.err = errFileTooLarge
		return int(c.left), c.err
	}
	c.left -= int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		c.err = err
	}
	return n, err
}
