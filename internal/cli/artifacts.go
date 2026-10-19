package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/urfave/cli/v3"

	"github.com/phinnerhq/sloppa/internal/api"
)

// artifactLine is one artifact per line, tab-separated: id, name, collection, link.
func artifactLine(a api.Artifact) string {
	return strings.Join([]string{a.ID, a.Name, a.Collection, a.RawURL}, "\t")
}

func artifactFields(a api.Artifact) string {
	return strings.Join([]string{
		"id: " + a.ID,
		"name: " + a.Name,
		"collection: " + a.Collection,
		"type: " + a.ContentType,
		"size: " + strconv.FormatInt(a.Size, 10),
		"created_at: " + isoTime(a.CreatedAt),
		"url: " + a.URL,
		"raw_url: " + a.RawURL,
	}, "\n")
}

var artifactID = regexp.MustCompile(`^(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// checkID refuses what can't be an artifact's id before asking the server about it.
func checkID(id string) error {
	if !artifactID.MatchString(id) {
		return userErrorf("Not an artifact id: %s; find ids with sloppa artifact list", id)
	}
	return nil
}

func noArtifact(id string) error {
	return userErrorf("No artifact %s: find ids with sloppa artifact list", id)
}

const collectionUsage = "The `collection`, e.g. \"owner/repo #42\" for a PR; the exact same label adds to it"

func collectionFlag(usage string) *cli.StringFlag {
	return &cli.StringFlag{Name: "collection", Aliases: []string{"c"}, Usage: usage}
}

// collectionOf is the --collection given, as the API takes it.
func collectionOf(cmd *cli.Command) (string, error) {
	collection := strings.TrimSpace(cmd.String("collection"))
	switch {
	case !cmd.IsSet("collection"):
		return "", userErrorf("Name a collection with -c, e.g. -c \"owner/repo #42\" for a PR")
	case collection == "":
		return "", userErrorf("--collection can't be empty: e.g. -c \"owner/repo #42\" for a PR")
	case utf8.RuneCountInString(collection) > api.CollectionMax:
		return "", userErrorf("--collection is at most %d characters", api.CollectionMax)
	}
	return collection, nil
}

// oneID is the single id a command takes.
func oneID(cmd *cli.Command) (string, error) {
	if cmd.Args().Len() != 1 {
		return "", userErrorf("Name one artifact by its id: find ids with sloppa artifact list")
	}
	id := cmd.Args().First()
	return id, checkID(id)
}

func (a *app) artifactCommand() *cli.Command {
	return &cli.Command{
		Name:  "artifact",
		Usage: "Upload, find, refile and delete artifacts",
		Commands: []*cli.Command{
			a.uploadCommand(),
			a.listCommand(),
			a.getCommand(),
			a.moveCommand(),
			a.deleteCommand(),
		},
	}
}

func (a *app) listCommand() *cli.Command {
	return &cli.Command{
		Name: "list",
		Usage: "List artifacts, newest first: one per line, tab-separated id, name, collection and link. " +
			"When there are more, the command for the next page goes to stderr.",
		Flags: []cli.Flag{
			collectionFlag("Only this `collection`, e.g. \"owner/repo #42\""),
			&cli.StringFlag{Name: "search", Aliases: []string{"q"}, Usage: "Part of the name, id, collection or type, any case"},
			&cli.IntFlag{Name: "limit", Usage: "At most this many, 1 to 200 (default 60)", HideDefault: true},
			&cli.StringFlag{Name: "cursor", Usage: "The next page, as printed after the last one"},
			jsonFlag(),
		},
		Metadata: helpSections(nil,
			example{`sloppa artifact list -c "owner/repo #42"`, "One PR's artifacts"},
			example{"sloppa artifact list -q checkout --limit 10", "Search by name, id, collection or type"},
			example{"sloppa artifact list --json | jq -r '.items[].rawUrl'", "Links only"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return userErrorf("artifact list takes no arguments: filter with -c and -q")
			}
			query := url.Values{}
			// The same flags again, for the next page's command.
			var same []string
			if cmd.IsSet("collection") {
				query.Set("collection", cmd.String("collection"))
				same = append(same, "-c", shellQuote(cmd.String("collection")))
			}
			if cmd.IsSet("search") {
				query.Set("q", cmd.String("search"))
				same = append(same, "-q", shellQuote(cmd.String("search")))
			}
			if cmd.IsSet("limit") {
				limit := cmd.Int("limit")
				if limit < 1 || limit > 200 {
					return userErrorf("--limit must be between 1 and 200")
				}
				query.Set("limit", strconv.Itoa(limit))
				same = append(same, "--limit", strconv.Itoa(limit))
			}
			if cmd.IsSet("cursor") {
				query.Set("cursor", cmd.String("cursor"))
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var page api.ArtifactPage
			raw, err := c.call(ctx, http.MethodGet, "/api/artifacts", query, nil, &page)
			if err != nil {
				return apiFailure("Couldn't list artifacts", err)
			}
			if cmd.Bool("json") {
				return printJSON(stdout(cmd), raw)
			}
			for _, artifact := range page.Items {
				fmt.Fprintln(stdout(cmd), artifactLine(artifact))
			}
			if page.NextCursor != "" {
				next := append(same, "--cursor", shellQuote(page.NextCursor))
				fmt.Fprintf(stderr(cmd), "more: sloppa artifact list %s\n", strings.Join(next, " "))
			}
			return nil
		},
	}
}

func getArtifact(ctx context.Context, c *client, id string) (api.Artifact, []byte, error) {
	var artifact api.Artifact
	raw, err := c.call(ctx, http.MethodGet, "/api/artifacts/"+url.PathEscape(id), nil, nil, &artifact)
	return artifact, raw, err
}

func (a *app) getCommand() *cli.Command {
	return &cli.Command{
		Name:      "get",
		Usage:     "Show one artifact: name, collection, type, size, upload time and links.",
		ArgsUsage: "<id>",
		Flags:     []cli.Flag{jsonFlag()},
		Metadata: helpSections(
			[]argument{{"id", "The artifact's id"}},
			example{"sloppa artifact get 3f2b8c1e-7d4a-4e0b-9c55-2a1f6e8d9b70", ""},
			example{"sloppa artifact get 3f2b8c1e-7d4a-4e0b-9c55-2a1f6e8d9b70 --json", ""},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			id, err := oneID(cmd)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			artifact, raw, err := getArtifact(ctx, c, id)
			if api.KindArtifactNotFound.Is(err) {
				return noArtifact(id)
			} else if err != nil {
				return apiFailure("Couldn't look it up", err)
			}
			if cmd.Bool("json") {
				return printJSON(stdout(cmd), raw)
			}
			fmt.Fprintln(stdout(cmd), artifactFields(artifact))
			return nil
		},
	}
}

func (a *app) moveCommand() *cli.Command {
	return &cli.Command{
		Name:      "move",
		Usage:     "File an artifact under another collection. Its links stay the same.",
		ArgsUsage: "<id>",
		Flags:     []cli.Flag{collectionFlag(collectionUsage), jsonFlag()},
		Metadata: helpSections(
			[]argument{{"id", "The artifact's id"}},
			example{`sloppa artifact move 3f2b8c1e-7d4a-4e0b-9c55-2a1f6e8d9b70 -c "owner/repo #43"`, "Fix a mistyped PR number"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			id, err := oneID(cmd)
			if err != nil {
				return err
			}
			collection, err := collectionOf(cmd)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var artifact api.Artifact
			raw, err := c.call(ctx, http.MethodPatch, "/api/artifacts/"+url.PathEscape(id), nil, api.MoveArtifact{Collection: collection}, &artifact)
			if api.KindArtifactNotFound.Is(err) {
				return noArtifact(id)
			} else if err != nil {
				return apiFailure("Couldn't move it", err)
			}
			if cmd.Bool("json") {
				return printJSON(stdout(cmd), raw)
			}
			fmt.Fprintln(stdout(cmd), artifactFields(artifact))
			return nil
		},
	}
}

func (a *app) deleteCommand() *cli.Command {
	return &cli.Command{
		Name:      "delete",
		Aliases:   []string{"rm"},
		Usage:     "Delete artifacts. Asks for --yes, since their links stop working everywhere.",
		ArgsUsage: "<ids>...",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "yes", Usage: "Delete them: their links stop working, in PRs too"},
			&cli.BoolFlag{Name: "dry-run", Usage: "Only show what would be deleted"},
		},
		Metadata: helpSections(
			[]argument{{"ids", "The artifacts' ids"}},
			example{"sloppa artifact delete 3f2b8c1e-7d4a-4e0b-9c55-2a1f6e8d9b70 --dry-run", "See what would go"},
			example{"sloppa artifact delete 3f2b8c1e-7d4a-4e0b-9c55-2a1f6e8d9b70 --yes", ""},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			ids := cmd.Args().Slice()
			if len(ids) == 0 {
				return userErrorf("Name the artifacts to delete by their ids: find ids with sloppa artifact list")
			}
			for _, id := range ids {
				if err := checkID(id); err != nil {
					return err
				}
			}
			out := stdout(cmd)
			if !cmd.Bool("dry-run") && !cmd.Bool("yes") {
				return userErrorf("Deleting breaks every link to these artifacts, in PRs too. Check with --dry-run, then confirm:\n  sloppa artifact delete %s --yes", strings.Join(ids, " "))
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if cmd.Bool("dry-run") {
				for _, id := range ids {
					artifact, _, err := getArtifact(ctx, c, id)
					switch {
					case api.KindArtifactNotFound.Is(err):
						fmt.Fprintf(out, "already gone: %s\n", id)
					case err != nil:
						return apiFailure("Couldn't look it up", err)
					default:
						fmt.Fprintf(out, "would delete: %s\n", artifactLine(artifact))
					}
				}
				return nil
			}
			for _, id := range ids {
				// Already gone is what was asked for, so a retry succeeds too.
				_, err := c.call(ctx, http.MethodDelete, "/api/artifacts/"+url.PathEscape(id), nil, nil, nil)
				switch {
				case api.KindArtifactNotFound.Is(err):
					fmt.Fprintf(out, "already gone: %s\n", id)
				case err != nil:
					return apiFailure("Couldn't delete "+id, err)
				default:
					fmt.Fprintf(out, "deleted: %s\n", id)
				}
			}
			return nil
		},
	}
}

func (a *app) collectionCommand() *cli.Command {
	return &cli.Command{
		Name:  "collection",
		Usage: "List collections",
		Commands: []*cli.Command{{
			Name:  "list",
			Usage: "List collections, most recently active first: one per line, tab-separated name, artifact count and last upload.",
			Flags: []cli.Flag{jsonFlag()},
			Metadata: helpSections(nil,
				example{"sloppa collection list", ""},
				example{"sloppa collection list --json", ""},
			),
			Action: func(ctx context.Context, cmd *cli.Command) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				var summaries []api.CollectionSummary
				raw, err := c.call(ctx, http.MethodGet, "/api/collections", nil, nil, &summaries)
				if err != nil {
					return apiFailure("Couldn't list collections", err)
				}
				if cmd.Bool("json") {
					return printJSON(stdout(cmd), raw)
				}
				for _, s := range summaries {
					fmt.Fprintf(stdout(cmd), "%s\t%d\t%s\n", s.Collection, s.Count, isoTime(s.LastUploadAt))
				}
				return nil
			},
		}},
	}
}
