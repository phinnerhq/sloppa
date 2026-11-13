package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/phinnerhq/sloppa/internal/api"
)

type agentsOverviewOutput struct {
	Body api.AgentsOverview
}

type instructionsBody struct {
	Content string `json:"content" doc:"Markdown, at most 256 KiB."`
}

type setInstructionsInput struct {
	Body instructionsBody
}

type setMachineInstructionsInput struct {
	MachineID string `path:"machineId"`
	Body      instructionsBody
}

type sourceBody struct {
	Source string `json:"source" minLength:"1" maxLength:"300" doc:"A GitHub repository, as owner/repo or its URL."`
}

type listSourceInput struct {
	Body sourceBody
}

type sourceListingOutput struct {
	Body api.SourceListing
}

type importSkillsInput struct {
	Body struct {
		Source string   `json:"source" minLength:"1" maxLength:"300" doc:"A GitHub repository, as owner/repo or its URL."`
		Names  []string `json:"names" minItems:"1" maxItems:"200" doc:"The skills to import, by name, as the listing gives them."`
	}
}

type skillsOutput struct {
	Body []api.Skill
}

type updateSkillsInput struct {
	Body struct {
		Names []string `json:"names,omitempty" required:"false" maxItems:"500" doc:"The skills to update; every imported one when left out."`
	}
}

type skillUpdatesOutput struct {
	Body api.SkillUpdates
}

type skillNameInput struct {
	Name string `path:"name" maxLength:"64"`
}

type putSkillInput struct {
	Name string `path:"name" maxLength:"64"`
	Body struct {
		Files []api.SkillFile `json:"files" minItems:"1" maxItems:"500" doc:"The skill's folder, with a SKILL.md at its root."`
	}
}

type configureSkillInput struct {
	Name string `path:"name" maxLength:"64"`
	Body struct {
		Enabled    *bool    `json:"enabled,omitempty" required:"false" doc:"Off takes it off every machine."`
		DisabledOn []string `json:"disabledOn,omitempty" required:"false" maxItems:"500" doc:"The machines it stays off, replacing those it did."`
	}
}

type skillOutput struct {
	Body api.Skill
}

type skillContentOutput struct {
	Body api.SkillContent
}

type syncPlanOutput struct {
	Body api.SyncPlan
}

type reportSyncInput struct {
	Body struct {
		Fingerprint string   `json:"fingerprint" maxLength:"128" doc:"The plan's that was applied."`
		Problems    []string `json:"problems" maxItems:"1000" doc:"What couldn't be done; none when everything went right."`
		CLIVersion  string   `json:"cliVersion,omitempty" maxLength:"100"`
	}
}

// skillUploadMax bounds a skill upload's body: its files as base64, and the JSON around them.
const skillUploadMax = api.SkillBytesMax/3*4 + 1<<20

func (s *server) registerAgents() {
	overview := s.operation("Agents", "agents.overview", http.MethodGet, "/api/agents", signedIn)
	overview.Description = "The instructions and skills `sloppa agents sync` brings to the machines, and how each machine last synced."
	huma.Register(s.api, overview, func(ctx context.Context, _ *struct{}) (*agentsOverviewOutput, error) {
		o, err := s.agents.Overview(ctx, account(ctx))
		if err != nil {
			return nil, err
		}
		return &agentsOverviewOutput{Body: o}, nil
	})

	setShared := s.operation("Agents", "agents.setInstructions", http.MethodPut, "/api/agents/instructions", signedIn)
	setShared.Description = "Replaces the instructions every machine gets in ~/.agents/AGENTS.md."
	setShared.MaxBodyBytes = api.InstructionsMax + 1<<16
	huma.Register(s.api, setShared, func(ctx context.Context, in *setInstructionsInput) (*struct{}, error) {
		return nil, s.agents.SetInstructions(ctx, account(ctx), in.Body.Content)
	})

	setOwn := s.operation("Agents", "agents.setMachineInstructions", http.MethodPut, "/api/agents/instructions/{machineId}", signedIn,
		api.KindMachineNotFound)
	setOwn.Description = "Replaces the instructions only this machine gets, after the shared ones. Blank removes them."
	setOwn.MaxBodyBytes = api.InstructionsMax + 1<<16
	huma.Register(s.api, setOwn, func(ctx context.Context, in *setMachineInstructionsInput) (*struct{}, error) {
		return nil, s.agents.SetMachineInstructions(ctx, account(ctx), in.MachineID, in.Body.Content)
	})

	list := s.operation("Agents", "agents.listSource", http.MethodPost, "/api/agents/sources", signedIn,
		api.KindSourceUnavailable)
	list.Description = "The skills in a GitHub repository's default branch, to pick from for importing."
	huma.Register(s.api, list, func(ctx context.Context, in *listSourceInput) (*sourceListingOutput, error) {
		l, err := s.agents.ListSource(ctx, account(ctx), in.Body.Source)
		if err != nil {
			return nil, err
		}
		return &sourceListingOutput{Body: l}, nil
	})

	imp := s.operation("Agents", "agents.importSkills", http.MethodPost, "/api/agents/skills/import", signedIn,
		api.KindSourceUnavailable)
	imp.Description = "Imports skills from a GitHub repository's default branch. A skill by the same name is replaced, and follows the repository from then on."
	huma.Register(s.api, imp, func(ctx context.Context, in *importSkillsInput) (*skillsOutput, error) {
		imported, err := s.agents.ImportSkills(ctx, account(ctx), in.Body.Source, in.Body.Names)
		if err != nil {
			return nil, err
		}
		return &skillsOutput{Body: imported}, nil
	})

	update := s.operation("Agents", "agents.updateSkills", http.MethodPost, "/api/agents/skills/update", signedIn,
		api.KindSkillNotFound)
	update.Description = "Brings imported skills up to date with their repositories. A repository that can't be read fails its skills only."
	huma.Register(s.api, update, func(ctx context.Context, in *updateSkillsInput) (*skillUpdatesOutput, error) {
		u, err := s.agents.UpdateSkills(ctx, account(ctx), in.Body.Names)
		if err != nil {
			return nil, err
		}
		return &skillUpdatesOutput{Body: u}, nil
	})

	put := s.operation("Agents", "agents.putSkill", http.MethodPut, "/api/agents/skills/{name}", signedIn)
	put.Description = "Saves a skill's folder as uploaded, replacing the skill by that name if there is one. It's then no longer updated from a repository."
	put.MaxBodyBytes = skillUploadMax
	huma.Register(s.api, put, func(ctx context.Context, in *putSkillInput) (*skillOutput, error) {
		k, err := s.agents.PutSkill(ctx, account(ctx), in.Name, in.Body.Files)
		if err != nil {
			return nil, err
		}
		return &skillOutput{Body: k}, nil
	})

	configure := s.operation("Agents", "agents.configureSkill", http.MethodPatch, "/api/agents/skills/{name}", signedIn,
		api.KindSkillNotFound, api.KindMachineNotFound)
	configure.Description = "Turns a skill on or off, everywhere or on some machines."
	huma.Register(s.api, configure, func(ctx context.Context, in *configureSkillInput) (*skillOutput, error) {
		k, err := s.agents.ConfigureSkill(ctx, account(ctx), in.Name, in.Body.Enabled, in.Body.DisabledOn)
		if err != nil {
			return nil, err
		}
		return &skillOutput{Body: k}, nil
	})

	del := s.operation("Agents", "agents.deleteSkill", http.MethodDelete, "/api/agents/skills/{name}", signedIn,
		api.KindSkillNotFound)
	del.Description = "Deletes a skill: machines remove it the next time they sync."
	huma.Register(s.api, del, func(ctx context.Context, in *skillNameInput) (*struct{}, error) {
		return nil, s.agents.DeleteSkill(ctx, account(ctx), in.Name)
	})

	files := s.operation("Agents", "agents.skillContent", http.MethodGet, "/api/agents/skills/{name}/files", signedIn,
		api.KindSkillNotFound)
	files.Description = "A skill's files, base64."
	huma.Register(s.api, files, func(ctx context.Context, in *skillNameInput) (*skillContentOutput, error) {
		c, err := s.agents.SkillContent(ctx, account(ctx), in.Name)
		if err != nil {
			return nil, err
		}
		return &skillContentOutput{Body: c}, nil
	})

	syncPlan := s.operation("Agents", "agents.syncPlan", http.MethodGet, "/api/agents/sync", machine)
	syncPlan.Description = "What the machine whose token this is should have: its composed instructions, and its skills by version."
	huma.Register(s.api, syncPlan, func(ctx context.Context, _ *struct{}) (*syncPlanOutput, error) {
		p, err := s.agents.Plan(ctx, account(ctx))
		if err != nil {
			return nil, err
		}
		return &syncPlanOutput{Body: p}, nil
	})

	report := s.operation("Agents", "agents.reportSync", http.MethodPost, "/api/agents/sync", machine)
	report.Description = "What `sloppa agents sync` did, for the dashboard to show."
	huma.Register(s.api, report, func(ctx context.Context, in *reportSyncInput) (*struct{}, error) {
		return nil, s.agents.Report(ctx, account(ctx), in.Body.Fingerprint, in.Body.Problems, in.Body.CLIVersion)
	})
}
