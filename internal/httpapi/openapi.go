package httpapi

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/phinnerhq/sloppa/internal/api"
)

// Lists are never null in responses: handlers send empty ones instead.
func init() { huma.DefaultArrayNullable = false }

// errorsKey is where an operation's metadata lists the api.ErrorKinds it answers with, besides the
// ones every operation like it may.
const errorsKey = "errors"

// operation is the start of a Huma operation: who may call it, and the errors it may answer with
// besides those any operation may.
func (s *server) operation(tag, id, method, path string, who access, errs ...api.ErrorKind) huma.Operation {
	op := huma.Operation{
		OperationID: id,
		Method:      method,
		Path:        path,
		Tags:        []string{tag},
		Security:    who.security(),
		Metadata:    map[string]any{errorsKey: errs},
	}
	if who != public {
		op.Middlewares = huma.Middlewares{s.requireAuth(who)}
	}
	return op
}

// documentErrors writes every operation's error responses into the document: one schema per kind of
// error, with its `code` a constant, and a union of them where a status has several, so a generated
// client can tell them apart by code. Huma's own guesses at the errors are replaced.
func documentErrors(doc *huma.OpenAPI) {
	schemas := doc.Components.Schemas.Map()
	delete(schemas, "Error")
	for _, kind := range api.ErrorKinds {
		schemas[kind.Name()] = errorSchema(kind)
	}
	for _, item := range doc.Paths {
		for method, op := range map[string]*huma.Operation{
			http.MethodGet: item.Get, http.MethodPost: item.Post, http.MethodPut: item.Put,
			http.MethodPatch: item.Patch, http.MethodDelete: item.Delete,
		} {
			if op == nil {
				continue
			}
			for status := range op.Responses {
				if code, err := strconv.Atoi(status); err != nil || code >= 400 {
					delete(op.Responses, status)
				}
			}
			byStatus := map[int][]api.ErrorKind{}
			for _, kind := range operationErrors(method, op) {
				if !slices.Contains(byStatus[kind.Status], kind) {
					byStatus[kind.Status] = append(byStatus[kind.Status], kind)
				}
			}
			for status, kinds := range byStatus {
				op.Responses[strconv.Itoa(status)] = errorResponse(status, kinds)
			}
		}
	}
}

// operationErrors is what op may fail with: what it lists, and what comes with its kind of operation.
func operationErrors(method string, op *huma.Operation) []api.ErrorKind {
	kinds := []api.ErrorKind{api.KindInternalError}
	if len(op.Parameters) > 0 || op.RequestBody != nil {
		kinds = append(kinds, api.KindBadRequest)
	}
	if op.RequestBody != nil {
		kinds = append(kinds, api.KindPayloadTooLarge)
	}
	if len(op.Security) > 0 {
		kinds = append(kinds, api.KindUnauthorized)
		// Unsafe requests signed in by cookie must come from the app itself.
		if method != http.MethodGet && slices.ContainsFunc(op.Security, func(r map[string][]string) bool {
			_, ok := r[cookieScheme]
			return ok
		}) {
			kinds = append(kinds, api.KindForbidden)
		}
	}
	listed, _ := op.Metadata[errorsKey].([]api.ErrorKind)
	return append(kinds, listed...)
}

func errorSchema(kind api.ErrorKind) *huma.Schema {
	s := &huma.Schema{
		Type: "object",
		Properties: map[string]*huma.Schema{
			"code":    {Type: "string", Const: kind.Code},
			"message": {Type: "string", Description: "What went wrong, for people."},
		},
		Required:             []string{"code", "message"},
		AdditionalProperties: false,
	}
	switch kind.Field {
	case "id":
		s.Properties["id"] = &huma.Schema{Type: "string"}
	case "retryAfter":
		s.Properties["retryAfter"] = &huma.Schema{Type: "integer", Minimum: new(1.0), Description: "Seconds until trying again is worth it, as in the Retry-After header."}
	}
	if kind.Field != "" {
		s.Required = append(s.Required, kind.Field)
	}
	return s
}

// errorResponse is the response for an error status, one of kinds.
func errorResponse(status int, kinds []api.ErrorKind) *huma.Response {
	slices.SortFunc(kinds, func(a, b api.ErrorKind) int { return cmp.Compare(a.Code, b.Code) })
	ref := func(kind api.ErrorKind) string { return "#/components/schemas/" + kind.Name() }
	schema := &huma.Schema{Ref: ref(kinds[0])}
	if len(kinds) > 1 {
		schema = &huma.Schema{Discriminator: &huma.Discriminator{PropertyName: "code", Mapping: map[string]string{}}}
		for _, kind := range kinds {
			schema.OneOf = append(schema.OneOf, &huma.Schema{Ref: ref(kind)})
			schema.Discriminator.Mapping[kind.Code] = ref(kind)
		}
	}
	return &huma.Response{
		Description: http.StatusText(status),
		Content:     map[string]*huma.MediaType{"application/json": {Schema: schema}},
	}
}
