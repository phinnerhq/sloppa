package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/phinnerhq/sloppa/internal/agentsync"
	"github.com/phinnerhq/sloppa/internal/api"
)

func (a *app) agentsCommand() *cli.Command {
	return &cli.Command{
		Name:     "agents",
		Usage:    "Sync agent instructions and skills from the server to this machine",
		Commands: []*cli.Command{a.agentsSyncCommand()},
	}
}

// syncOutput is what `sloppa agents sync --json` prints.
type syncOutput struct {
	Fingerprint string   `json:"fingerprint"`
	DryRun      bool     `json:"dryRun"`
	Changes     []string `json:"changes"`
	Problems    []string `json:"problems"`
	Backup      string   `json:"backup,omitempty"`
}

func (a *app) agentsSyncCommand() *cli.Command {
	return &cli.Command{
		Name: "sync",
		Usage: "Bring this machine's agent setup in line with the dashboard's Agents page: instructions in ~/.agents/AGENTS.md, " +
			"linked as ~/.claude/CLAUDE.md, ~/.codex/AGENTS.md and ~/.config/opencode/AGENTS.md, and skills in ~/.agents/skills, " +
			"linked into ~/.claude/skills. Only harnesses whose folder exists are linked, and only skills sloppa installed are removed. " +
			"Whatever is replaced or removed is moved to ~/.agents/.sloppa-backups first. Prints one line per change.",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "dry-run", Aliases: []string{"n"}, Usage: "Say what would change, changing nothing"},
			jsonFlag(),
		},
		Metadata: helpSections(nil,
			example{"sloppa agents sync", ""},
			example{"sloppa agents sync --dry-run", "What syncing would change"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return userErrorf("Couldn't tell this user's home folder: %v", err)
			}
			var plan api.SyncPlan
			if _, err := c.call(ctx, http.MethodGet, "/api/agents/sync", nil, nil, &plan); err != nil {
				return apiFailure("Couldn't get this machine's agent setup", err)
			}
			dryRun := cmd.Bool("dry-run")
			res := agentsync.Sync(ctx, plan, agentsync.Options{
				Home:      home,
				Harnesses: agentsync.Harnesses(home, os.Getenv),
				DryRun:    dryRun,
				Now:       time.Now(),
				Fetch: func(ctx context.Context, name string) (api.SkillContent, error) {
					var content api.SkillContent
					_, err := c.call(ctx, http.MethodGet, "/api/agents/skills/"+url.PathEscape(name)+"/files", nil, nil, &content)
					return content, err
				},
			})
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !dryRun {
				report := map[string]any{"fingerprint": plan.Fingerprint, "problems": nonNilStrings(res.Problems), "cliVersion": a.version}
				if _, err := c.call(ctx, http.MethodPost, "/api/agents/sync", nil, report, nil); err != nil {
					res.Problems = append(res.Problems, apiFailure("tell the server how it went", err).Error())
				}
			}

			if cmd.Bool("json") {
				raw, err := json.Marshal(syncOutput{
					Fingerprint: plan.Fingerprint, DryRun: dryRun, Changes: nonNilStrings(res.Changes),
					Problems: nonNilStrings(res.Problems), Backup: res.Backup,
				})
				if err != nil {
					return err
				}
				if err := printJSON(stdout(cmd), raw); err != nil {
					return err
				}
			} else {
				if dryRun && len(res.Changes) > 0 {
					fmt.Fprintln(stderr(cmd), "Dry run: nothing changed. Syncing would have done this:")
				}
				for _, change := range res.Changes {
					fmt.Fprintln(stdout(cmd), change)
				}
				if len(res.Changes) == 0 {
					fmt.Fprintf(stderr(cmd), "Up to date: %s\n", summary(plan))
				}
				if res.Backup != "" {
					fmt.Fprintf(stderr(cmd), "Moved what was replaced to %s\n", res.Backup)
				}
			}
			if len(res.Problems) > 0 {
				return userErrorf("Couldn't sync everything:\n  %s", strings.Join(res.Problems, "\n  "))
			}
			return nil
		},
	}
}

// summary is what a plan holds, in a few words.
func summary(plan api.SyncPlan) string {
	instructions := "no instructions"
	if plan.Instructions != "" {
		instructions = "instructions"
	}
	skills := fmt.Sprintf("%d skills", len(plan.Skills))
	if len(plan.Skills) == 1 {
		skills = "1 skill"
	}
	return instructions + " and " + skills
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
