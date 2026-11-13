package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/phinnerhq/sloppa/internal/agents"
	"github.com/phinnerhq/sloppa/internal/api"
)

// What the dashboard's Agents page edits, from the command line.

func (a *app) agentsStatusCommand() *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "List the machines and how each last synced: one per line, tab-separated name, state (in sync, needs a sync, problems, never synced) and when.",
		Flags: []cli.Flag{jsonFlag()},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			o, raw, err := a.overview(ctx)
			if err != nil {
				return err
			}
			if cmd.Bool("json") {
				return printJSON(stdout(cmd), raw)
			}
			for _, m := range o.Machines {
				state, at := "never synced", ""
				if m.LastSync != nil {
					at = isoTime(m.LastSync.At)
					switch {
					case m.LastSync.Fingerprint != m.Fingerprint:
						state = "needs a sync"
					case len(m.LastSync.Problems) > 0:
						state = "problems: " + strings.Join(m.LastSync.Problems, "; ")
					default:
						state = "in sync"
					}
				}
				fmt.Fprintln(stdout(cmd), strings.Join([]string{m.Name, state, at}, "\t"))
			}
			return nil
		},
	}
}

// overview is the Agents page's data, as decoded and as it came.
func (a *app) overview(ctx context.Context) (api.AgentsOverview, json.RawMessage, error) {
	var o api.AgentsOverview
	c, err := a.client()
	if err != nil {
		return o, nil, err
	}
	raw, err := c.call(ctx, http.MethodGet, "/api/agents", nil, nil, &o)
	if err != nil {
		return o, nil, apiFailure("Couldn't read the agent setup", err)
	}
	return o, raw, nil
}

// machineFlag names a machine, by name or id, or `this` for the one the CLI runs on.
func machineFlag(usage string) cli.Flag {
	return &cli.StringFlag{Name: "machine", Aliases: []string{"m"}, Usage: usage + " (a name or id, or `this`)"}
}

// findMachine is the machine named by --machine: `this`, an id, or a name only one machine has.
func (a *app) findMachine(ctx context.Context, machines []api.MachineAgents, name string) (api.MachineAgents, error) {
	if name == "this" {
		c, err := a.client()
		if err != nil {
			return api.MachineAgents{}, err
		}
		user, _, err := c.session(ctx)
		if err != nil {
			return api.MachineAgents{}, apiFailure("Couldn't tell which machine this is", err)
		}
		if user.Machine == "" {
			return api.MachineAgents{}, userErrorf("--machine this: signed in without a machine's token; name the machine instead")
		}
		name = user.Machine
	}
	if i := slices.IndexFunc(machines, func(m api.MachineAgents) bool { return m.MachineID == name }); i >= 0 {
		return machines[i], nil
	}
	var found []api.MachineAgents
	for _, m := range machines {
		if strings.EqualFold(m.Name, name) {
			found = append(found, m)
		}
	}
	var names []string
	for _, m := range machines {
		names = append(names, m.Name)
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return api.MachineAgents{}, userErrorf("No machine %q: there's %s", name, strings.Join(names, ", "))
	}
	return api.MachineAgents{}, userErrorf("Several machines are called %q: pass one's id, from sloppa agents status --json", name)
}

func (a *app) instructionsCommand() *cli.Command {
	return &cli.Command{
		Name:     "instructions",
		Usage:    "Show or replace the instructions every machine gets, or one machine's own",
		Commands: []*cli.Command{a.instructionsShowCommand(), a.instructionsSetCommand()},
	}
}

func (a *app) instructionsShowCommand() *cli.Command {
	return &cli.Command{
		Name:  "show",
		Usage: "Print the shared instructions, or with --machine that machine's own, or with --composed what it gets in ~/.agents/AGENTS.md.",
		Flags: []cli.Flag{
			machineFlag("Print this machine's own instead"),
			&cli.BoolFlag{Name: "composed", Usage: "With --machine: the shared ones and the machine's, as it gets them"},
		},
		Metadata: helpSections(nil,
			example{"sloppa agents instructions show > shared.md", ""},
			example{"sloppa agents instructions show --machine this --composed", "What this machine gets"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			o, _, err := a.overview(ctx)
			if err != nil {
				return err
			}
			text := o.Instructions
			if cmd.IsSet("machine") {
				m, err := a.findMachine(ctx, o.Machines, cmd.String("machine"))
				if err != nil {
					return err
				}
				text = m.Instructions
				if cmd.Bool("composed") {
					text = api.ComposeInstructions(o.Instructions, m.Instructions)
				}
			} else if cmd.Bool("composed") {
				return userErrorf("--composed needs --machine")
			}
			_, err = io.WriteString(stdout(cmd), text)
			return err
		},
	}
}

func (a *app) instructionsSetCommand() *cli.Command {
	return &cli.Command{
		Name: "set",
		Usage: "Replace the shared instructions, or with --machine that machine's own, with a file's contents or stdin's (-). " +
			"Machines get them on their next sync. An empty file removes a machine's own.",
		ArgsUsage: "<file | ->",
		Flags:     []cli.Flag{machineFlag("Replace this machine's own instead")},
		Metadata: helpSections(
			[]argument{{"file", "A Markdown file, or - for stdin"}},
			example{"sloppa agents instructions set shared.md", ""},
			example{"sloppa agents instructions show > a.md && $EDITOR a.md && sloppa agents instructions set a.md", "Edit them"},
			example{"printf '## Safety\\n\\n- Never bind 0.0.0.0\\n' | sloppa agents instructions set --machine this -", "This machine's own"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 1 {
				return userErrorf("Name one file to read the instructions from, or - for stdin")
			}
			var content []byte
			var err error
			if path := cmd.Args().First(); path == "-" {
				content, err = io.ReadAll(io.LimitReader(cmd.Root().Reader, api.InstructionsMax+1))
			} else {
				content, err = os.ReadFile(path)
			}
			if err != nil {
				return userErrorf("Couldn't read the instructions: %v", err)
			}
			if len(content) > api.InstructionsMax {
				return userErrorf("Instructions are at most %d KiB", api.InstructionsMax>>10)
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			path, who := "/api/agents/instructions", "every machine"
			if cmd.IsSet("machine") {
				o, _, err := a.overview(ctx)
				if err != nil {
					return err
				}
				m, err := a.findMachine(ctx, o.Machines, cmd.String("machine"))
				if err != nil {
					return err
				}
				path, who = path+"/"+url.PathEscape(m.MachineID), m.Name
			}
			if _, err := c.call(ctx, http.MethodPut, path, nil, map[string]string{"content": string(content)}, nil); err != nil {
				return apiFailure("Couldn't save the instructions", err)
			}
			fmt.Fprintf(stdout(cmd), "saved the instructions for %s: run `sloppa agents sync` on each machine to bring them over\n", who)
			return nil
		},
	}
}

func (a *app) skillCommand() *cli.Command {
	return &cli.Command{
		Name:  "skill",
		Usage: "List, add, update, turn on or off and remove the skills every machine gets",
		Commands: []*cli.Command{
			a.skillListCommand(), a.skillShowCommand(), a.skillPushCommand(), a.skillImportCommand(), a.skillUpdateCommand(),
			a.skillSwitchCommand(true), a.skillSwitchCommand(false), a.skillRemoveCommand(),
		},
	}
}

// skillState is where a skill is on, in a few words.
func skillState(k api.Skill, machines []api.MachineAgents) string {
	if !k.Enabled {
		return "off"
	}
	if len(k.DisabledOn) == 0 {
		return "on"
	}
	var off []string
	for _, m := range machines {
		if slices.Contains(k.DisabledOn, m.MachineID) {
			off = append(off, m.Name)
		}
	}
	return "on, off on " + strings.Join(off, ", ")
}

func (a *app) skillListCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List the skills, by name: one per line, tab-separated name, source (owner/repo, or uploaded), where it's on, and description.",
		Flags: []cli.Flag{jsonFlag()},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			o, raw, err := a.overview(ctx)
			if err != nil {
				return err
			}
			if cmd.Bool("json") {
				var skills struct {
					Skills json.RawMessage `json:"skills"`
				}
				if err := json.Unmarshal(raw, &skills); err != nil {
					return err
				}
				return printJSON(stdout(cmd), skills.Skills)
			}
			for _, k := range o.Skills {
				fmt.Fprintln(stdout(cmd), strings.Join([]string{k.Name, cmp.Or(k.Source, "uploaded"), skillState(k, o.Machines), k.Description}, "\t"))
			}
			return nil
		},
	}
}

func (a *app) skillShowCommand() *cli.Command {
	return &cli.Command{
		Name:      "show",
		Usage:     "Print a skill's SKILL.md, or another of its files; --files lists them instead.",
		ArgsUsage: "<name> [file]",
		Flags:     []cli.Flag{&cli.BoolFlag{Name: "files", Usage: "List its files, one per line"}},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() < 1 || cmd.Args().Len() > 2 {
				return userErrorf("Name a skill, and optionally one of its files: sloppa agents skill show <name> [file]")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			name := cmd.Args().First()
			var content api.SkillContent
			if _, err := c.call(ctx, http.MethodGet, "/api/agents/skills/"+url.PathEscape(name)+"/files", nil, nil, &content); err != nil {
				return apiFailure("Couldn't read "+name, err)
			}
			if cmd.Bool("files") {
				for _, f := range content.Files {
					fmt.Fprintln(stdout(cmd), f.Path)
				}
				return nil
			}
			want := cmp.Or(cmd.Args().Get(1), agents.SkillFile)
			for _, f := range content.Files {
				if f.Path == want {
					_, err := stdout(cmd).Write(f.Content)
					return err
				}
			}
			return userErrorf("%s has no file %s: list them with --files", name, want)
		},
	}
}

// skippedNames are never part of a pushed skill.
var skippedNames = map[string]bool{".git": true, "node_modules": true, ".DS_Store": true}

// readSkill is the files of the skill folder dir, the name it goes by, and the links in it, which are
// left out: one could point at anything on this machine, like a key. Files are read through a root at
// dir, so nothing outside it is read even if the folder changes meanwhile.
func readSkill(dir string) (name string, files []api.SkillFile, links []string, err error) {
	// The folder itself may be a link, as the ones sync makes in ~/.claude/skills: what it points at
	// is what was named.
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return "", nil, nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", nil, nil, err
	}
	defer root.Close()
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skippedNames[d.Name()] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			links = append(links, p)
			return nil
		case !d.Type().IsRegular():
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		content, err := root.ReadFile(p)
		if err != nil {
			return err
		}
		files = append(files, api.SkillFile{Path: p, Executable: info.Mode()&0o100 != 0, Content: content})
		return nil
	})
	if err != nil {
		return "", nil, nil, err
	}
	var skillMD []byte
	for _, f := range files {
		if f.Path == agents.SkillFile {
			skillMD = f.Content
		}
	}
	abs, _ := filepath.Abs(dir)
	fmName, _ := agents.Frontmatter(skillMD)
	return agents.SkillName(fmName, filepath.Base(abs)), files, links, nil
}

// skillDirs are the skill folders in path: itself when it has a SKILL.md, otherwise each of its
// folders that has one, like ~/.agents/skills.
func skillDirs(path string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(path, agents.SkillFile)); err == nil {
		return []string{path}, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		dir := filepath.Join(path, e.Name())
		if _, err := os.Stat(filepath.Join(dir, agents.SkillFile)); err == nil {
			dirs = append(dirs, dir)
		}
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("no %s in %s, nor in any folder of it", agents.SkillFile, path)
	}
	return dirs, nil
}

func (a *app) skillPushCommand() *cli.Command {
	return &cli.Command{
		Name: "push",
		Usage: "Upload skill folders, each with a SKILL.md, or folders of them, like ~/.agents/skills. Each replaces the skill by that name, " +
			"which then is no longer updated from GitHub. Named by SKILL.md's frontmatter, or the folder. Links in a folder are left out. Prints one line per skill.",
		ArgsUsage: "<folders>...",
		Flags:     []cli.Flag{&cli.StringFlag{Name: "name", Usage: "Upload one folder under this name instead"}},
		Metadata: helpSections(
			[]argument{{"folders", "Skill folders, or folders of skill folders"}},
			example{"sloppa agents skill push ./skills/review", ""},
			example{"sloppa agents skill push ~/.agents/skills", "Every skill in it"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			paths := cmd.Args().Slice()
			if len(paths) == 0 {
				return userErrorf("Name the skill folders to upload: sloppa agents skill push <folders>...")
			}
			var dirs []string
			for _, p := range paths {
				found, err := skillDirs(p)
				if err != nil {
					return userErrorf("Couldn't find skills: %v", err)
				}
				dirs = append(dirs, found...)
			}
			if cmd.IsSet("name") && len(dirs) != 1 {
				return userErrorf("--name takes exactly one skill folder, not %d", len(dirs))
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var failed []string
			for _, dir := range dirs {
				name, files, links, err := readSkill(dir)
				if err != nil {
					failed = append(failed, fmt.Sprintf("%s: %v", dir, err))
					continue
				}
				for _, link := range links {
					fmt.Fprintf(stderr(cmd), "left out %s: links aren't pushed, copy the file in instead\n", filepath.Join(dir, link))
				}
				name = cmp.Or(cmd.String("name"), name)
				var k api.Skill
				if _, err := c.call(ctx, http.MethodPut, "/api/agents/skills/"+url.PathEscape(name), nil, map[string]any{"files": files}, &k); err != nil {
					failed = append(failed, apiFailure(fmt.Sprintf("%s (from %s)", name, dir), err).Error())
					continue
				}
				fmt.Fprintf(stdout(cmd), "pushed: %s\t%.12s\t%d files\n", k.Name, k.Hash, k.Files)
			}
			if len(failed) > 0 {
				return userErrorf("Couldn't push everything:\n  %s", strings.Join(failed, "\n  "))
			}
			return nil
		},
	}
}

func (a *app) skillImportCommand() *cli.Command {
	return &cli.Command{
		Name: "import",
		Usage: "Import skills from a GitHub repository's default branch; they can be updated from it later. " +
			"Without names, lists the repository's skills instead: tab-separated name, folder, whether you have it, and description.",
		ArgsUsage: "<owner/repo> [names]...",
		Flags:     []cli.Flag{&cli.BoolFlag{Name: "all", Usage: "Import every skill in it"}},
		Metadata: helpSections(
			[]argument{{"owner/repo", "The repository, or its GitHub URL"}, {"names", "Its skills to import"}},
			example{"sloppa agents skill import mattpocock/skills", "See what it has"},
			example{"sloppa agents skill import mattpocock/skills research writing-for-agents", ""},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() < 1 {
				return userErrorf("Name a GitHub repository: sloppa agents skill import <owner/repo> [names]...")
			}
			source, names := cmd.Args().First(), cmd.Args().Tail()
			c, err := a.client()
			if err != nil {
				return err
			}
			if len(names) == 0 {
				var l api.SourceListing
				if _, err := c.call(ctx, http.MethodPost, "/api/agents/sources", nil, map[string]string{"source": source}, &l); err != nil {
					return apiFailure("Couldn't list "+source+"'s skills", err)
				}
				if !cmd.Bool("all") {
					for _, k := range l.Skills {
						have := ""
						if k.Imported {
							have = "have it"
						}
						fmt.Fprintln(stdout(cmd), strings.Join([]string{k.Name, k.Path, have, k.Description}, "\t"))
					}
					return nil
				}
				source = l.Source
				for _, k := range l.Skills {
					names = append(names, k.Name)
				}
			}
			var imported []api.Skill
			body := map[string]any{"source": source, "names": names}
			if _, err := c.call(ctx, http.MethodPost, "/api/agents/skills/import", nil, body, &imported); err != nil {
				return apiFailure("Couldn't import from "+source, err)
			}
			for _, k := range imported {
				fmt.Fprintf(stdout(cmd), "imported: %s\t%.12s\n", k.Name, k.Hash)
			}
			return nil
		},
	}
}

func (a *app) skillUpdateCommand() *cli.Command {
	return &cli.Command{
		Name:      "update",
		Usage:     "Update imported skills from their GitHub repositories, all of them unless named. Prints updated:, unchanged: or failed: per skill.",
		ArgsUsage: "[names]...",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var u api.SkillUpdates
			body := map[string]any{}
			if names := cmd.Args().Slice(); len(names) > 0 {
				body["names"] = names
			}
			if _, err := c.call(ctx, http.MethodPost, "/api/agents/skills/update", nil, body, &u); err != nil {
				return apiFailure("Couldn't update skills", err)
			}
			for _, name := range u.Updated {
				fmt.Fprintf(stdout(cmd), "updated: %s\n", name)
			}
			for _, name := range u.Unchanged {
				fmt.Fprintf(stdout(cmd), "unchanged: %s\n", name)
			}
			var failed []string
			for _, f := range u.Failed {
				fmt.Fprintf(stdout(cmd), "failed: %s\n", f.Name)
				failed = append(failed, f.Name+": "+f.Message)
			}
			if len(failed) > 0 {
				return userErrorf("Couldn't update everything:\n  %s", strings.Join(failed, "\n  "))
			}
			return nil
		},
	}
}

func (a *app) skillSwitchCommand(on bool) *cli.Command {
	name, usage := "disable", "Turn skills off: everywhere, or with --machine only there"
	if on {
		name, usage = "enable", "Turn skills on: everywhere, or with --machine on machines they were turned off on"
	}
	return &cli.Command{
		Name:      name,
		Usage:     usage + ". Machines follow on their next sync.",
		ArgsUsage: "<names>...",
		Flags:     []cli.Flag{&cli.StringSliceFlag{Name: "machine", Aliases: []string{"m"}, Usage: "Only on this machine (a name or id, or `this`); repeatable"}},
		Metadata: helpSections(nil,
			example{"sloppa agents skill " + name + " review", ""},
			example{"sloppa agents skill " + name + " review --machine this", "On this machine only"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			skills := cmd.Args().Slice()
			if len(skills) == 0 {
				return userErrorf("Name the skills to %s", name)
			}
			o, _, err := a.overview(ctx)
			if err != nil {
				return err
			}
			var ids []string
			for _, m := range cmd.StringSlice("machine") {
				found, err := a.findMachine(ctx, o.Machines, m)
				if err != nil {
					return err
				}
				ids = append(ids, found.MachineID)
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			for _, skill := range skills {
				i := slices.IndexFunc(o.Skills, func(k api.Skill) bool { return k.Name == skill })
				if i < 0 {
					return userErrorf("No skill %s: list them with sloppa agents skill list", skill)
				}
				k := o.Skills[i]
				body := map[string]any{"enabled": on}
				switch {
				case len(ids) == 0:
				case !on:
					body = map[string]any{"disable": ids}
				case !k.Enabled:
					// Off everywhere until now: on only on the machines named.
					var off []string
					for _, m := range o.Machines {
						if !slices.Contains(ids, m.MachineID) {
							off = append(off, m.MachineID)
						}
					}
					body["disabledOn"] = nonNilStrings(off)
				default:
					body["enable"] = ids
				}
				var saved api.Skill
				if _, err := c.call(ctx, http.MethodPatch, "/api/agents/skills/"+url.PathEscape(skill), nil, body, &saved); err != nil {
					return apiFailure("Couldn't change "+skill, err)
				}
				fmt.Fprintf(stdout(cmd), "%s\t%s\n", saved.Name, skillState(saved, o.Machines))
			}
			return nil
		},
	}
}

func (a *app) skillRemoveCommand() *cli.Command {
	return &cli.Command{
		Name:      "remove",
		Usage:     "Remove skills: every machine removes them on its next sync. Asks for --yes.",
		ArgsUsage: "<names>...",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "yes", Usage: "Remove them, from every machine"},
			&cli.BoolFlag{Name: "dry-run", Usage: "Only show what would be removed"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			names := cmd.Args().Slice()
			if len(names) == 0 {
				return userErrorf("Name the skills to remove")
			}
			if !cmd.Bool("dry-run") && !cmd.Bool("yes") {
				return userErrorf("Removing takes the skills off every machine. Check with --dry-run, then confirm:\n  sloppa agents skill remove %s --yes", strings.Join(names, " "))
			}
			o, _, err := a.overview(ctx)
			if err != nil {
				return err
			}
			for _, name := range names {
				if !slices.ContainsFunc(o.Skills, func(k api.Skill) bool { return k.Name == name }) {
					return userErrorf("No skill %s: list them with sloppa agents skill list", name)
				}
			}
			if cmd.Bool("dry-run") {
				for _, name := range names {
					fmt.Fprintf(stdout(cmd), "would remove: %s\n", name)
				}
				return nil
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			for _, name := range names {
				_, err := c.call(ctx, http.MethodDelete, "/api/agents/skills/"+url.PathEscape(name), nil, nil, nil)
				switch {
				case api.KindSkillNotFound.Is(err):
					fmt.Fprintf(stdout(cmd), "gone already: %s\n", name)
				case err != nil:
					return apiFailure("Couldn't remove "+name, err)
				default:
					fmt.Fprintf(stdout(cmd), "removed: %s\n", name)
				}
			}
			return nil
		},
	}
}
