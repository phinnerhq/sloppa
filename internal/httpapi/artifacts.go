package httpapi

import (
	"context"
	"net/http"
	"regexp"

	"github.com/danielgtaylor/huma/v2"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/artifacts"
)

// uuidPattern is what an artifact id looks like; anything else is no artifact's.
const uuidPattern = `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`

var artifactID = regexp.MustCompile(uuidPattern)

type listInput struct {
	Collection string `query:"collection"`
	Q          string `query:"q" doc:"Matches name, id, collection or content type, case-insensitively."`
	Cursor     string `query:"cursor" doc:"A previous page's nextCursor."`
	Limit      int    `query:"limit" minimum:"1" maximum:"200"`
}

type pageOutput struct {
	Body api.ArtifactPage
}

type mosaicInput struct {
	Cursor string `query:"cursor" doc:"A previous page's nextCursor."`
	Limit  int    `query:"limit" minimum:"1" maximum:"100"`
}

type groupPageOutput struct {
	Body api.GroupPage
}

type collectionsOutput struct {
	Body []api.CollectionSummary
}

type statsOutput struct {
	Body api.Stats
}

type artifactIDInput struct {
	ID string `path:"id" pattern:"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$" patternDescription:"a UUID"`
}

type moveInput struct {
	ID   string `path:"id" pattern:"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$" patternDescription:"a UUID"`
	Body struct {
		Collection string `json:"collection" minLength:"1" maxLength:"200" doc:"Filed under this collection, e.g. \"owner/repo #42\"."`
	}
}

type artifactOutput struct {
	Body api.Artifact
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func (s *server) registerArtifacts() {
	huma.Register(s.api, s.operation("Artifacts", "artifacts.list", http.MethodGet, "/api/artifacts", signedIn),
		func(ctx context.Context, in *listInput) (*pageOutput, error) {
			limit := in.Limit
			if limit == 0 {
				limit = api.PageSize
			}
			page, err := s.artifacts.List(ctx, artifacts.ListQuery{Collection: in.Collection, Q: in.Q, Cursor: in.Cursor, Limit: limit})
			if err != nil {
				return nil, err
			}
			page.Items = nonNil(page.Items)
			return &pageOutput{Body: page}, nil
		})

	huma.Register(s.api, s.operation("Artifacts", "artifacts.mosaic", http.MethodGet, "/api/collections/mosaic", signedIn),
		func(ctx context.Context, in *mosaicInput) (*groupPageOutput, error) {
			limit := in.Limit
			if limit == 0 {
				limit = api.GroupPageSize
			}
			page, err := s.artifacts.Mosaic(ctx, in.Cursor, limit)
			if err != nil {
				return nil, err
			}
			page.Items = nonNil(page.Items)
			for i := range page.Items {
				page.Items[i].Preview = nonNil(page.Items[i].Preview)
			}
			return &groupPageOutput{Body: page}, nil
		})

	huma.Register(s.api, s.operation("Artifacts", "artifacts.collections", http.MethodGet, "/api/collections", signedIn),
		func(ctx context.Context, _ *struct{}) (*collectionsOutput, error) {
			collections, err := s.artifacts.Collections(ctx)
			if err != nil {
				return nil, err
			}
			return &collectionsOutput{Body: nonNil(collections)}, nil
		})

	huma.Register(s.api, s.operation("Artifacts", "artifacts.stats", http.MethodGet, "/api/stats", signedIn),
		func(ctx context.Context, _ *struct{}) (*statsOutput, error) {
			stats, err := s.artifacts.Stats(ctx)
			if err != nil {
				return nil, err
			}
			return &statsOutput{Body: stats}, nil
		})

	get := s.operation("Artifacts", "artifacts.get", http.MethodGet, "/api/artifacts/{id}", public,
		http.StatusNotFound, http.StatusBadGateway)
	get.Description = "Needs no sign-in: the random id is what makes a shared link unguessable."
	huma.Register(s.api, get, func(ctx context.Context, in *artifactIDInput) (*artifactOutput, error) {
		a, err := s.artifacts.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &artifactOutput{Body: a}, nil
	})

	move := s.operation("Artifacts", "artifacts.move", http.MethodPatch, "/api/artifacts/{id}", signedIn,
		http.StatusNotFound, http.StatusBadGateway)
	move.Description = "Files an artifact under another collection; its links stay the same."
	huma.Register(s.api, move, func(ctx context.Context, in *moveInput) (*artifactOutput, error) {
		collection, err := trimmedName("collection", in.Body.Collection, api.CollectionMax)
		if err != nil {
			return nil, err
		}
		a, err := s.artifacts.Move(ctx, in.ID, collection)
		if err != nil {
			return nil, err
		}
		return &artifactOutput{Body: a}, nil
	})

	huma.Register(s.api, s.operation("Artifacts", "artifacts.delete", http.MethodDelete, "/api/artifacts/{id}", signedIn,
		http.StatusNotFound, http.StatusBadGateway),
		func(ctx context.Context, in *artifactIDInput) (*struct{}, error) {
			return nil, s.artifacts.Remove(ctx, in.ID)
		})
}
