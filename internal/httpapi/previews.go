package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/phinnerhq/sloppa/internal/api"
)

type previewsOutput struct {
	Body []api.Preview
}

// startPreviewBody is api.StartPreview with its bounds, which the tests hold to the api constants.
type startPreviewBody struct {
	Name     string `json:"name,omitempty" pattern:"^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$" patternDescription:"lowercase letters, digits and dashes, at most 40, not starting or ending with a dash" doc:"The subdomain, instead of the random id."`
	Password string `json:"password,omitempty" minLength:"1" maxLength:"200" doc:"Asked for by the proxy as Basic auth, any user name."`
	TTL      int    `json:"ttl,omitempty" minimum:"60" maximum:"86400" doc:"Seconds until the preview stops by itself; 8 hours when left out."`
	Port     int    `json:"port" minimum:"1" maximum:"65535" doc:"The dev server's, on the machine running the command."`
	Command  string `json:"command,omitempty" maxLength:"4096"`
	Cwd      string `json:"cwd,omitempty" maxLength:"4096"`
	Hostname string `json:"hostname,omitempty" maxLength:"255"`
}

type startPreviewInput struct {
	Body startPreviewBody
}

type startedPreviewOutput struct {
	Body api.StartedPreview
}

type previewIDInput struct {
	ID string `path:"id"`
}

func (s *server) registerPreviews() {
	huma.Register(s.api, s.operation("Previews", "previews.list", http.MethodGet, "/api/previews", signedIn,
		http.StatusServiceUnavailable),
		func(ctx context.Context, _ *struct{}) (*previewsOutput, error) {
			previews, err := s.previews.List(ctx, account(ctx))
			if err != nil {
				return nil, err
			}
			return &previewsOutput{Body: nonNil(previews)}, nil
		})

	start := s.operation("Previews", "previews.start", http.MethodPost, "/api/previews", signedIn,
		http.StatusConflict, http.StatusServiceUnavailable)
	start.Description = "Makes a preview `sloppa preview run` then connects its tunnel to, with the same credentials."
	huma.Register(s.api, start, func(ctx context.Context, in *startPreviewInput) (*startedPreviewOutput, error) {
		b := in.Body
		started, err := s.previews.Start(ctx, account(ctx), api.StartPreview{
			Name: b.Name, Password: b.Password, TTL: b.TTL, Port: b.Port, Command: b.Command, Cwd: b.Cwd, Hostname: b.Hostname,
		})
		if err != nil {
			return nil, err
		}
		return &startedPreviewOutput{Body: started}, nil
	})

	huma.Register(s.api, s.operation("Previews", "previews.stop", http.MethodDelete, "/api/previews/{id}", signedIn,
		http.StatusNotFound, http.StatusServiceUnavailable),
		func(ctx context.Context, in *previewIDInput) (*struct{}, error) {
			return nil, s.previews.Stop(ctx, account(ctx), in.ID)
		})
}

// registerTunnel serves the WebSocket `sloppa preview run` connects its tunnel on. It isn't JSON,
// so it's a plain route, written into the OpenAPI document by hand.
func (s *server) registerTunnel(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/previews/{id}/tunnel", s.withAuth(signedIn, func(w http.ResponseWriter, r *http.Request) {
		s.previews.Tunnel(w, r, r.PathValue("id"))
	}))
	s.api.OpenAPI().AddOperation(&huma.Operation{
		OperationID: "previews.tunnel",
		Method:      http.MethodGet,
		Path:        "/api/previews/{id}/tunnel",
		Tags:        []string{"Previews"},
		Summary:     "Connect a preview's tunnel",
		Description: "A WebSocket (subprotocol sloppa-tunnel.v1) carrying yamux, from `sloppa preview run` to the server, " +
			"with the credentials that started the preview.",
		Security: signedIn.security(),
		Parameters: []*huma.Param{
			{Name: "id", In: "path", Required: true, Schema: &huma.Schema{Type: "string"}},
		},
		Responses: map[string]*huma.Response{
			"101": {Description: "Switching Protocols"},
			"401": s.errorResponse(http.StatusUnauthorized),
			"404": s.errorResponse(http.StatusNotFound),
			"503": s.errorResponse(http.StatusServiceUnavailable),
		},
	})
}

// errorResponse documents an api.Error answer, for operations written into the document by hand.
func (s *server) errorResponse(status int) *huma.Response {
	return &huma.Response{
		Description: http.StatusText(status),
		Content: map[string]*huma.MediaType{
			"application/json": {Schema: s.api.OpenAPI().Components.Schemas.Schema(errorType, true, "")},
		},
	}
}
