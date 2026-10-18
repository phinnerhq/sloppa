// Package httpapi is the server's HTTP side: the typed JSON API under /api (described at
// /api/openapi.json), raw files and thumbnails, setup links, the dashboard, and in front of it all,
// the preview proxy for preview hosts.
package httpapi

import (
	"net/http"
	"net/url"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/phinnerhq/sloppa/internal/config"
)

type server struct {
	cfg       *config.Config
	cookie    string // the session cookie's name
	accounts  Accounts
	artifacts Artifacts
	previews  Previews
	api       huma.API
	cop       *http.CrossOriginProtection
}

// New is the whole app as one handler.
func New(d Deps) http.Handler {
	s := &server{
		cfg:       d.Config,
		cookie:    sessionCookieName(d.Config.PublicURL),
		accounts:  d.Accounts,
		artifacts: d.Artifacts,
		previews:  d.Previews,
		cop:       http.NewCrossOriginProtection(),
	}
	// Behind a proxy that rewrites Host, a browser's Origin is still the public URL's.
	if public, err := url.Parse(d.Config.PublicURL); err == nil {
		s.cop.AddTrustedOrigin(public.Scheme + "://" + public.Host)
	}

	mux := http.NewServeMux()
	s.api = humago.New(mux, humaConfig(s.cookie))
	s.registerSession()
	s.registerAgents()
	s.registerArtifacts()
	s.registerPreviews()
	s.registerUpload(mux)
	s.registerTunnel(mux)
	s.registerFiles(mux)
	s.registerSetup(mux)
	mux.Handle("GET /", static(d.Static))
	documentBadRequests(s.api.OpenAPI())

	return logRequests(recoverPanics(s.previewHosts(mux)))
}

func humaConfig(cookie string) huma.Config {
	c := huma.DefaultConfig("sloppa", "1")
	// No `$schema` fields or Link headers in responses: the dashboard's client takes the shapes as
	// they are.
	c.CreateHooks = nil
	c.OpenAPIPath = "/api/openapi"
	c.DocsPath = ""
	c.SchemasPath = ""
	c.Formats = map[string]huma.Format{"application/json": jsonFormat, "json": jsonFormat}
	c.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		cookieScheme: {Type: "apiKey", Name: cookie, In: "cookie"},
		bearerScheme: {Type: "http", Scheme: "Bearer"},
	}
	return c
}

// previewHosts hands requests for a preview's host to the preview proxy before any app route sees
// them: a preview is someone's dev server, and none of the app's routes or cookies belong there.
func (s *server) previewHosts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.previews != nil && s.previews.IsPreviewHost(r.Host) {
			s.previews.ServeProxy(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// operation is the start of a Huma operation: who may call it, and the errors it may answer with
// besides those.
func (s *server) operation(tag, id, method, path string, who access, statuses ...int) huma.Operation {
	op := huma.Operation{
		OperationID: id,
		Method:      method,
		Path:        path,
		Tags:        []string{tag},
		Security:    who.security(),
		Errors:      statuses,
	}
	if who != public {
		op.Errors = append([]int{http.StatusUnauthorized}, op.Errors...)
		op.Middlewares = huma.Middlewares{s.requireAuth(who)}
	}
	return op
}

// documentBadRequests lists Huma's 422 for invalid input as the 400 it's sent as.
func documentBadRequests(doc *huma.OpenAPI) {
	for _, item := range doc.Paths {
		for _, op := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op == nil {
				continue
			}
			if r, ok := op.Responses["422"]; ok {
				delete(op.Responses, "422")
				r.Description = http.StatusText(http.StatusBadRequest)
				op.Responses["400"] = r
			}
		}
	}
}
