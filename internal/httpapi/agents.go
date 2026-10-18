package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/phinnerhq/sloppa/internal/api"
)

type agentsOutput struct {
	Body []api.Agent
}

type agentName struct {
	Name string `json:"name" minLength:"1" maxLength:"100" doc:"What the agent calls itself, e.g. \"Claude Code on my laptop\"."`
}

type createAgentInput struct {
	Body agentName
}

type createdAgentOutput struct {
	Body api.CreatedAgent
}

type idInput struct {
	ID string `path:"id"`
}

type renameAgentInput struct {
	ID   string `path:"id"`
	Body agentName
}

type setupLinkOutput struct {
	Body api.SetupLink
}

// trimmedName is name without surrounding space, refused when that leaves nothing or too much.
func trimmedName(field, name string, maxLen int) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", api.BadRequest("body." + field + ": expected a non-blank string")
	}
	if utf8.RuneCountInString(name) > maxLen {
		return "", api.BadRequest("body." + field + ": expected length <= " + strconv.Itoa(maxLen))
	}
	return name, nil
}

func (s *server) registerAgents() {
	huma.Register(s.api, s.operation("Agents", "agents.list", http.MethodGet, "/api/agents", browser),
		func(ctx context.Context, _ *struct{}) (*agentsOutput, error) {
			agents, err := s.accounts.Agents(ctx, account(ctx))
			if err != nil {
				return nil, err
			}
			return &agentsOutput{Body: nonNil(agents)}, nil
		})

	create := s.operation("Agents", "agents.create", http.MethodPost, "/api/agents", browser)
	create.Description = "A token made by hand, for a script or an app the setup prompt doesn't fit. Shown once."
	huma.Register(s.api, create, func(ctx context.Context, in *createAgentInput) (*createdAgentOutput, error) {
		name, err := trimmedName("name", in.Body.Name, api.AgentNameMax)
		if err != nil {
			return nil, err
		}
		created, err := s.accounts.CreateAgent(ctx, account(ctx), name)
		if err != nil {
			return nil, err
		}
		return &createdAgentOutput{Body: created}, nil
	})

	huma.Register(s.api, s.operation("Agents", "agents.revoke", http.MethodDelete, "/api/agents/{id}", browser),
		func(ctx context.Context, in *idInput) (*struct{}, error) {
			return nil, s.accounts.RevokeAgent(ctx, account(ctx), in.ID)
		})

	huma.Register(s.api, s.operation("Agents", "agents.rename", http.MethodPatch, "/api/agents/{id}", browser),
		func(ctx context.Context, in *renameAgentInput) (*struct{}, error) {
			name, err := trimmedName("name", in.Body.Name, api.AgentNameMax)
			if err != nil {
				return nil, err
			}
			return nil, s.accounts.RenameAgent(ctx, account(ctx), in.ID, name)
		})

	createSetup := s.operation("Agents", "agents.createSetup", http.MethodPost, "/api/setup-links", browser)
	createSetup.Description = "A link any number of agents can trade for a token of their own for 30 minutes, with the prompt that has them do it."
	huma.Register(s.api, createSetup, func(ctx context.Context, _ *struct{}) (*setupLinkOutput, error) {
		id, code, expires, err := s.accounts.CreateSetup(ctx, account(ctx))
		if err != nil {
			return nil, err
		}
		url := s.cfg.PublicURL + api.SetupPath(code)
		return &setupLinkOutput{Body: api.SetupLink{ID: id, URL: url, Prompt: setupPrompt(s.cfg.PublicURL, url), ExpiresAt: api.Time(expires)}}, nil
	})

	huma.Register(s.api, s.operation("Agents", "agents.revokeSetup", http.MethodDelete, "/api/setup-links/{id}", browser),
		func(ctx context.Context, in *idInput) (*struct{}, error) {
			return nil, s.accounts.RevokeSetupByID(ctx, account(ctx), in.ID)
		})
}
