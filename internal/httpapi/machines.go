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

type machinesOutput struct {
	Body []api.Machine
}

type machineName struct {
	Name string `json:"name" minLength:"1" maxLength:"100" doc:"What the dashboard calls the machine, e.g. \"my laptop\"."`
}

type createMachineInput struct {
	Body machineName
}

type createdMachineOutput struct {
	Body api.CreatedMachine
}

type idInput struct {
	ID string `path:"id"`
}

type renameMachineInput struct {
	ID   string `path:"id"`
	Body machineName
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

func (s *server) registerMachines() {
	huma.Register(s.api, s.operation("Machines", "machines.list", http.MethodGet, "/api/machines", browser),
		func(ctx context.Context, _ *struct{}) (*machinesOutput, error) {
			machines, err := s.accounts.Machines(ctx, account(ctx))
			if err != nil {
				return nil, err
			}
			return &machinesOutput{Body: nonNil(machines)}, nil
		})

	create := s.operation("Machines", "machines.create", http.MethodPost, "/api/machines", browser)
	create.Description = "A machine made by hand, for a script or a computer the setup prompt doesn't fit. Its token is shown once."
	huma.Register(s.api, create, func(ctx context.Context, in *createMachineInput) (*createdMachineOutput, error) {
		name, err := trimmedName("name", in.Body.Name, api.MachineNameMax)
		if err != nil {
			return nil, err
		}
		created, err := s.accounts.CreateMachine(ctx, account(ctx), name)
		if err != nil {
			return nil, err
		}
		return &createdMachineOutput{Body: created}, nil
	})

	revoke := s.operation("Machines", "machines.revoke", http.MethodDelete, "/api/machines/{id}", browser)
	revoke.Description = "Deletes the machine and its token, which ends the previews it started."
	huma.Register(s.api, revoke, func(ctx context.Context, in *idInput) (*struct{}, error) {
		return nil, s.accounts.RevokeMachine(ctx, account(ctx), in.ID)
	})

	huma.Register(s.api, s.operation("Machines", "machines.rename", http.MethodPatch, "/api/machines/{id}", browser),
		func(ctx context.Context, in *renameMachineInput) (*struct{}, error) {
			name, err := trimmedName("name", in.Body.Name, api.MachineNameMax)
			if err != nil {
				return nil, err
			}
			return nil, s.accounts.RenameMachine(ctx, account(ctx), in.ID, name)
		})

	createSetup := s.operation("Machines", "machines.createSetup", http.MethodPost, "/api/setup-links", browser)
	createSetup.Description = "A link that registers one machine, with `sloppa setup`, within 30 minutes, and the prompt that has an agent do it."
	huma.Register(s.api, createSetup, func(ctx context.Context, _ *struct{}) (*setupLinkOutput, error) {
		id, code, expires, err := s.accounts.CreateSetup(ctx, account(ctx))
		if err != nil {
			return nil, err
		}
		url := s.cfg.PublicURL + api.SetupPath(code)
		return &setupLinkOutput{Body: api.SetupLink{ID: id, URL: url, Prompt: setupPrompt(url), ExpiresAt: api.Time(expires)}}, nil
	})

	huma.Register(s.api, s.operation("Machines", "machines.revokeSetup", http.MethodDelete, "/api/setup-links/{id}", browser),
		func(ctx context.Context, in *idInput) (*struct{}, error) {
			return nil, s.accounts.RevokeSetupByID(ctx, account(ctx), in.ID)
		})
}
