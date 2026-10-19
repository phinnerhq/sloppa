package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/phinnerhq/sloppa/internal/api"
)

// file is a file in the test's own folder, holding content.
func file(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// rows is the CLI's text output: one tab-separated line per item.
func rows(stdout string) [][]string {
	var out [][]string
	for line := range strings.SplitSeq(strings.TrimSpace(stdout), "\n") {
		if line != "" {
			out = append(out, strings.Split(line, "\t"))
		}
	}
	return out
}

func TestUploadSendsTheCollectionFirstThenEveryFileIntact(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	png := file(t, "shot.png", []byte{0x89, 0x50, 0x4e, 0x47, 1, 2, 3})
	big := make([]byte, 3<<20)
	rand.Read(big)
	trace := file(t, "trace.bin", big)
	log := file(t, "run.log", []byte("line one\nline two\n"))

	res := run(t, "", "artifact", "upload", "-c", "o/r #1", png, trace, log)

	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	_, uploads := fake.stored()
	parts := uploads[0]
	if len(parts) != 4 || parts[0].field != "collection" || string(parts[0].data) != "o/r #1" {
		t.Fatalf("parts: %d, first %q", len(parts), parts[0].field)
	}
	for i, want := range []struct {
		name string
		data []byte
	}{{"shot.png", []byte{0x89, 0x50, 0x4e, 0x47, 1, 2, 3}}, {"trace.bin", big}, {"run.log", []byte("line one\nline two\n")}} {
		if p := parts[i+1]; p.field != "files" || p.filename != want.name || !bytes.Equal(p.data, want.data) {
			t.Errorf("part %d: %s %s, %d bytes", i+1, p.field, p.filename, len(p.data))
		}
	}
	lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
	link := regexp.MustCompile(`^(!?)\[([^\]]+)\]\(` + regexp.QuoteMeta(fake.URL) + `/f/[0-9a-f-]+/([^)]+)\)$`)
	for i, want := range []struct{ embed, name string }{{"!", "shot.png"}, {"", "trace.bin"}, {"", "run.log"}} {
		m := link.FindStringSubmatch(lines[i])
		if m == nil || m[1] != want.embed || m[2] != want.name || m[3] != want.name {
			t.Errorf("line %d: %q", i, lines[i])
		}
	}
}

func TestUploadPrintsTheAPIsJSON(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)

	res := run(t, "", "artifact", "upload", "--json", "-c", "o/r #2", file(t, "notes.md", []byte("# notes")))

	var artifacts []api.Artifact
	if err := json.Unmarshal([]byte(res.stdout), &artifacts); err != nil || len(artifacts) != 1 {
		t.Fatalf("%v: %s", err, res.stdout)
	}
	if a := artifacts[0]; a.Name != "notes.md" || a.RawURL != fake.URL+"/f/"+a.ID+"/notes.md" || a.Collection != "o/r #2" {
		t.Errorf("%+v", a)
	}
	if !strings.HasPrefix(res.stdout, "[\n  {\n    \"id\": ") {
		t.Errorf("not indented like the API's JSON: %s", res.stdout)
	}
}

func TestUploadReadsStdinForDash(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)

	res := run(t, "from a pipe\n", "artifact", "upload", "-c", "o/r #6", "--name", "piped.log", "-")

	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	_, uploads := fake.stored()
	if p := uploads[0][1]; p.filename != "piped.log" || string(p.data) != "from a pipe\n" {
		t.Errorf("%s: %q", p.filename, p.data)
	}
	if !strings.HasPrefix(res.stdout, "[piped.log](") {
		t.Errorf("stdout %q", res.stdout)
	}
}

// readingStdin closes reading once read from.
type readingStdin struct {
	io.Reader
	once    sync.Once
	reading chan struct{}
}

func (r *readingStdin) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.reading) })
	return r.Reader.Read(p)
}

// Ctrl-C ends a command waiting on stdin, though the signal is caught and a producer holds the pipe
// open: the read isn't waited for.
func TestStdinReadsGiveWayToSignals(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	for _, args := range [][]string{
		{"artifact", "upload", "-c", "o/r #6", "--name", "piped.log", "-"},
		{"login", fake.URL},
	} {
		t.Run(args[0], func(t *testing.T) {
			pipe, producer := io.Pipe()
			defer producer.Close()
			stdin := &readingStdin{Reader: pipe, reading: make(chan struct{})}
			ctx, cancel := context.WithCancelCause(t.Context())
			r := startReading(ctx, stdin, args...)
			<-stdin.reading
			cancel(&interrupted{signal: os.Interrupt})
			if res := r.wait(t); res.code != 130 {
				t.Errorf("exit %d: %s", res.code, res.stderr)
			}
			if fake.requested("POST /api/artifacts") {
				t.Error("uploaded what stdin held so far")
			}
		})
	}
}

func TestUploadSendsNothingUnlessEveryFileIsThere(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	fine := file(t, "fine.txt", []byte("x"))
	missing := filepath.Join(t.TempDir(), "missing.txt")

	for _, tc := range []struct {
		args []string
		says string
	}{
		{[]string{"-c", "o/r #5", fine, missing}, "Not a file: " + missing},
		{[]string{"-c", "o/r #5", fine, t.TempDir()}, "Not a file: "},
		{[]string{"-c", "o/r #7", "-"}, "--name report.md -"},
		{[]string{"-c", "o/r #7", "--name", "a.txt", "-", "-"}, "Stdin can be read once"},
		{[]string{fine}, "Name a collection with -c"},
		{[]string{"-c", "  ", fine}, "--collection can't be empty"},
		{[]string{"-c", strings.Repeat("x", 201), fine}, "--collection is at most 200 characters"},
		{[]string{"-c", "o/r #5"}, "Name the files to upload"},
	} {
		res := run(t, "x", append([]string{"artifact", "upload"}, tc.args...)...)
		if res.code != 1 || !strings.Contains(res.stderr, tc.says) {
			t.Errorf("%v: %+v", tc.args, res)
		}
	}
	if fake.requested("POST /api/artifacts") {
		t.Error("uploaded something")
	}
}

func TestUploadRefusedToken(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	t.Setenv("SLOPPA_TOKEN", "not-a-token")

	res := run(t, "", "artifact", "upload", "-c", "o/r #3", file(t, "refused.txt", []byte("x")))

	want := "Upload failed: the server refused this machine's token. Set it up again with a new setup link, or:\n  " + signInAgain + "\n"
	if res.code != 1 || res.stderr != want {
		t.Errorf("%+v", res)
	}
	if stored, _ := fake.stored(); len(stored) != 0 {
		t.Error("stored it anyway")
	}
}

func TestUploadUnreachableServer(t *testing.T) {
	isolate(t)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	nowhere := "http://" + l.Addr().String()
	l.Close()
	t.Setenv("SLOPPA_URL", nowhere)
	t.Setenv("SLOPPA_TOKEN", "t")

	res := run(t, "", "artifact", "upload", "-c", "o/r #4", file(t, "lost.txt", []byte("x")))

	if res.code != 1 || !strings.HasPrefix(res.stderr, "Upload failed: couldn't reach "+nowhere+": ") {
		t.Errorf("%+v", res)
	}
}

func TestListPagesWithTheNextCommandOnStderr(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	one, two, three := fake.artifact("one.txt", "o/r #40", 1), fake.artifact("two.txt", "o/r #40", 1), fake.artifact("three.txt", "o/r #40", 1)
	fake.add(one, two, three, fake.artifact("elsewhere.txt", "o/r #41", 1))

	first := run(t, "", "artifact", "list", "-c", "o/r #40", "--limit", "2")

	next := regexp.MustCompile(`(?m)^more: sloppa (.+)$`).FindStringSubmatch(first.stderr)
	// The printed command, as a shell would split it: the collection is quoted for its space and #.
	if next == nil || next[1] != "artifact list -c 'o/r #40' --limit 2 --cursor 2" {
		t.Fatalf("stderr %q", first.stderr)
	}
	words := regexp.MustCompile(`'[^']*'|\S+`).FindAllString(next[1], -1)
	for i, word := range words {
		words[i] = strings.Trim(word, "'")
	}
	second := run(t, "", words...)
	if strings.Contains(second.stderr, "more:") {
		t.Errorf("a third page: %s", second.stderr)
	}
	listed := append(rows(first.stdout), rows(second.stdout)...)
	want := [][]string{
		{three.ID, "three.txt", "o/r #40", three.RawURL},
		{two.ID, "two.txt", "o/r #40", two.RawURL},
		{one.ID, "one.txt", "o/r #40", one.RawURL},
	}
	if len(listed) != 3 || strings.Join(listed[0], "\t") != strings.Join(want[0], "\t") || listed[2][0] != one.ID {
		t.Errorf("listed %q", listed)
	}

	searched := run(t, "", "artifact", "list", "-q", "ELSE", "--json")
	var page api.ArtifactPage
	if err := json.Unmarshal([]byte(searched.stdout), &page); err != nil || len(page.Items) != 1 || page.Items[0].Name != "elsewhere.txt" {
		t.Errorf("%v: %s", err, searched.stdout)
	}

	if res := run(t, "", "artifact", "list", "--limit", "0"); res.code != 1 || !strings.Contains(res.stderr, "--limit must be between 1 and 200") {
		t.Errorf("--limit 0: %+v", res)
	}
}

func TestGetShowsAnArtifactsFields(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	a := fake.artifact("shown.txt", "o/r #41", 3)
	fake.add(a)

	shown := run(t, "", "artifact", "get", a.ID)
	missing := run(t, "", "artifact", "get", "00000000-0000-4000-8000-000000000000")
	bad := run(t, "", "artifact", "get", "nope")
	asJSON := run(t, "", "artifact", "get", a.ID, "--json")

	want := strings.Join([]string{
		"id: " + a.ID,
		"name: shown.txt",
		"collection: o/r #41",
		"type: text/plain",
		"size: 3",
		"created_at: " + a.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
		"url: " + a.URL,
		"raw_url: " + a.RawURL,
	}, "\n") + "\n"
	if shown.stdout != want {
		t.Errorf("got\n%s\nwant\n%s", shown.stdout, want)
	}
	if missing.code != 1 || !strings.Contains(missing.stderr, "No artifact 00000000-0000-4000-8000-000000000000: find ids with sloppa artifact list") {
		t.Errorf("missing: %+v", missing)
	}
	if bad.code != 1 || !strings.Contains(bad.stderr, "Not an artifact id: nope") {
		t.Errorf("bad id: %+v", bad)
	}
	var got api.Artifact
	if json.Unmarshal([]byte(asJSON.stdout), &got); got.ID != a.ID {
		t.Errorf("--json: %s", asJSON.stdout)
	}
}

func TestMoveFilesAnArtifactElsewhere(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	a := fake.artifact("moved.txt", "o/r #42", 1)
	fake.add(a)

	res := run(t, "", "artifact", "move", a.ID, "-c", "o/r #43")
	missing := run(t, "", "artifact", "move", "00000000-0000-4000-8000-000000000000", "-c", "o/r #43")
	noCollection := run(t, "", "artifact", "move", a.ID)

	if stored, _ := fake.stored(); res.code != 0 || !strings.Contains(res.stdout, "collection: o/r #43\n") || stored[0].Collection != "o/r #43" {
		t.Errorf("%+v", res)
	}
	if missing.code != 1 || !strings.Contains(missing.stderr, "find ids with sloppa artifact list") {
		t.Errorf("missing: %+v", missing)
	}
	if noCollection.code != 1 {
		t.Errorf("no collection: %+v", noCollection)
	}
}

func TestDeleteWantsYesShowsDryRunsAndSucceedsAgain(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	a := fake.artifact("doomed.txt", "o/r #44", 1)
	fake.add(a)

	refused := run(t, "", "artifact", "delete", a.ID)
	dry := run(t, "", "artifact", "delete", a.ID, "--dry-run")
	stillThere, _ := fake.stored()
	deleted := run(t, "", "artifact", "delete", a.ID, "--yes")
	left, _ := fake.stored()
	again := run(t, "", "artifact", "rm", a.ID, "--yes")
	dryAgain := run(t, "", "artifact", "delete", "--dry-run", a.ID)

	if refused.code != 1 || !strings.Contains(refused.stderr, "Check with --dry-run, then confirm:\n  sloppa artifact delete "+a.ID+" --yes") {
		t.Errorf("refused: %+v", refused)
	}
	if dry.stdout != "would delete: "+artifactLine(a)+"\n" || len(stillThere) != 1 {
		t.Errorf("dry run: %+v", dry)
	}
	if deleted.stdout != "deleted: "+a.ID+"\n" || len(left) != 0 {
		t.Errorf("deleted: %+v", deleted)
	}
	if again.code != 0 || again.stdout != "already gone: "+a.ID+"\n" {
		t.Errorf("again: %+v", again)
	}
	if dryAgain.stdout != "already gone: "+a.ID+"\n" {
		t.Errorf("dry run again: %+v", dryAgain)
	}
}

func TestCollectionList(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	fake.add(fake.artifact("a.txt", "o/r #45", 1), fake.artifact("b.txt", "o/r #45", 1))
	stored, _ := fake.stored()
	last := stored[0].CreatedAt

	text := run(t, "", "collection", "list")
	asJSON := run(t, "", "collection", "list", "--json")

	if text.stdout != "o/r #45\t2\t"+isoTime(last)+"\n" {
		t.Errorf("%q", text.stdout)
	}
	var summaries []api.CollectionSummary
	if json.Unmarshal([]byte(asJSON.stdout), &summaries); len(summaries) != 1 || summaries[0].Count != 2 {
		t.Errorf("%s", asJSON.stdout)
	}
}

func TestShellQuote(t *testing.T) {
	for word, want := range map[string]string{
		"plain":       "plain",
		"o/r#1":       "'o/r#1'",
		"o/r #40":     "'o/r #40'",
		"it's":        `'it'\''s'`,
		"a=b,c:d@e%f": "a=b,c:d@e%f",
		"":            "''",
	} {
		if got := shellQuote(word); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", word, got, want)
		}
	}
}

func TestHelpHasExamples(t *testing.T) {
	isolate(t)

	res := run(t, "", "artifact", "upload", "--help")

	for _, want := range []string{
		"ARGUMENTS:\n   files  Files to upload; - reads stdin, named by --name",
		"   # Output piped in, under a name of your choosing\n   pnpm test 2>&1 | sloppa artifact upload -c \"owner/repo #42\" --name test-run.log -\n",
		"--collection collection, -c collection",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("no %q in\n%s", want, res.stdout)
		}
	}
	if typo := run(t, "", "artifact", "list", "--bogus"); typo.code != 1 || !strings.Contains(typo.stderr, "Run `sloppa artifact list --help`") {
		t.Errorf("typo: %+v", typo)
	}
	if unknown := run(t, "", "artifact", "nope"); unknown.code != 1 || !strings.Contains(unknown.stderr, `No command "nope" in sloppa artifact`) {
		t.Errorf("unknown: %+v", unknown)
	}
}
