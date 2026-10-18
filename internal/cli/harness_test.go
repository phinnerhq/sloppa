package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/urfave/cli/v3"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/tunnel"
)

// The test binary is also the processes the tests start: the CLI itself, for tests that signal it,
// and the dev server a preview runs, with a grandchild that lingers like a watcher.
const roleEnv = "SLOPPA_TEST_ROLE"

func TestMain(m *testing.M) {
	switch os.Getenv(roleEnv) {
	case "cli":
		// What the CLI starts in turn, its dev server.
		os.Setenv(roleEnv, os.Getenv(roleEnv+"_NEXT"))
		App(nil, "test").Run(context.Background(), append([]string{"sloppa"}, os.Args[1:]...))
		os.Exit(0)
	case "devserver":
		devServerRole()
	case "linger":
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	case "echoargs":
		json.NewEncoder(os.Stdout).Encode(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// devServerRole is a dev server for the tests: it reads PORT and HOST like a well-behaved one (or
// FIXTURE_PORT, like a misconfigured one), writes its pid and its grandchild's to FIXTURE_PIDS, and
// with FIXTURE_STUBBORN set ignores SIGTERM.
func devServerRole() {
	if os.Getenv("FIXTURE_STUBBORN") != "" {
		signal.Ignore(syscall.SIGTERM)
	}
	self, _ := os.Executable()
	grandchild := exec.Command(self)
	grandchild.Env = append(os.Environ(), roleEnv+"=linger")
	if err := grandchild.Start(); err != nil {
		panic(err)
	}
	port := os.Getenv("FIXTURE_PORT")
	if port == "" {
		port = os.Getenv("PORT")
	}
	if pids := os.Getenv("FIXTURE_PIDS"); pids != "" {
		os.WriteFile(pids, fmt.Appendf(nil, "%d %d", os.Getpid(), grandchild.Process.Pid), 0o600)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/exit", func(w http.ResponseWriter, r *http.Request) {
		code, _ := strconv.Atoi(r.URL.Query().Get("code"))
		w.WriteHeader(http.StatusNoContent)
		go func() {
			time.Sleep(50 * time.Millisecond)
			os.Exit(code)
		}()
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"path":       r.URL.Path,
			"host":       r.Host,
			"body":       len(body),
			"previewUrl": os.Getenv("PREVIEW_URL"),
			"previewId":  os.Getenv("PREVIEW_ID"),
			"args":       os.Args[1:],
		})
	})
	host := os.Getenv("HOST")
	l, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		panic(err)
	}
	fmt.Printf("fixture listening on %s\n", port)
	http.Serve(l, mux)
}

// lockedBuffer is written by the CLI and the processes it starts at once.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type result struct {
	code           int
	stdout, stderr string
}

// running is a command in progress, run in-process.
type running struct {
	stdout, stderr *lockedBuffer
	done           chan result
}

// start runs the CLI in-process with stdin, exiting like App would but returning the code instead.
func start(ctx context.Context, stdin string, args ...string) *running {
	return startReading(ctx, strings.NewReader(stdin), args...)
}

// startReading is start with stdin read from a reader of the test's.
func startReading(ctx context.Context, stdin io.Reader, args ...string) *running {
	app := newApp(nil, "test")
	app.ExitErrHandler = func(context.Context, *cli.Command, error) {}
	r := &running{stdout: &lockedBuffer{}, stderr: &lockedBuffer{}, done: make(chan result, 1)}
	app.Reader = stdin
	app.Writer, app.ErrWriter = r.stdout, r.stderr
	go func() {
		err := app.Run(ctx, append([]string{"sloppa"}, args...))
		code := report(r.stderr, err)
		r.done <- result{code: code, stdout: r.stdout.String(), stderr: r.stderr.String()}
	}()
	return r
}

func (r *running) wait(t *testing.T) result {
	t.Helper()
	select {
	case res := <-r.done:
		return res
	case <-time.After(30 * time.Second):
		t.Fatalf("the command didn't end; it said:\n%s", r.stderr.String())
		return result{}
	}
}

func run(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	return start(t.Context(), stdin, args...).wait(t)
}

// eventually fails the test unless check holds within 15 seconds.
func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// isolate keeps credentials to the test: no stored login, no environment, until it sets them.
func isolate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	t.Setenv("APPDATA", home+"/appdata")
	t.Setenv("SLOPPA_URL", "")
	t.Setenv("SLOPPA_TOKEN", "")
	t.Setenv("NO_COLOR", "1")
}

// part is a multipart part as the fake server received it, in order.
type part struct {
	field, filename string
	data            []byte
}

type fakePreview struct {
	api.Preview
	start   api.StartPreview
	tunnels chan *tunnel.Server
}

// fakeAPI is the sloppa API as the CLI sees it, with what it was sent kept for the tests.
type fakeAPI struct {
	*httptest.Server
	token string

	mu        sync.Mutex
	artifacts []api.Artifact
	parts     [][]part
	previews  map[string]*fakePreview
	requests  []string
	// expiresIn is how long previews last, from when they're started.
	expiresIn time.Duration
	// refuseTunnels answers tunnel requests with this status instead, when set.
	refuseTunnels int
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{token: "sloppa_test", previews: map[string]*fakePreview{}, expiresIn: time.Hour}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, api.User{Username: "test"})
	})
	mux.HandleFunc("POST /api/artifacts", f.upload)
	mux.HandleFunc("GET /api/artifacts", f.list)
	mux.HandleFunc("GET /api/artifacts/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.withArtifact(w, r, func(a *api.Artifact) { writeJSON(w, 200, a) })
	})
	mux.HandleFunc("PATCH /api/artifacts/{id}", func(w http.ResponseWriter, r *http.Request) {
		var move api.MoveArtifact
		json.NewDecoder(r.Body).Decode(&move)
		f.withArtifact(w, r, func(a *api.Artifact) {
			a.Collection = move.Collection
			writeJSON(w, 200, a)
		})
	})
	mux.HandleFunc("DELETE /api/artifacts/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.withArtifact(w, r, func(a *api.Artifact) {
			f.artifacts = slices.DeleteFunc(f.artifacts, func(b api.Artifact) bool { return b.ID == a.ID })
			w.WriteHeader(http.StatusNoContent)
		})
	})
	mux.HandleFunc("GET /api/collections", f.collections)
	mux.HandleFunc("GET /api/previews", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		list := []api.Preview{}
		for _, p := range f.previews {
			list = append(list, p.Preview)
		}
		writeJSON(w, 200, list)
	})
	mux.HandleFunc("POST /api/previews", f.startPreview)
	mux.HandleFunc("DELETE /api/previews/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.previews[r.PathValue("id")]; !ok {
			writeError(w, api.PreviewNotFound(r.PathValue("id")))
			return
		}
		delete(f.previews, r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/previews/{id}/tunnel", f.tunnel)
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			writeError(w, api.Unauthorized("Sign in, or pass an agent token"))
			return
		}
		if r.Header.Get("User-Agent") != "sloppa/test" {
			writeError(w, api.BadRequest("unexpected User-Agent "+r.Header.Get("User-Agent")))
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

// signIn points the CLI at the fake through the environment.
func (f *fakeAPI) signIn(t *testing.T) {
	isolate(t)
	t.Setenv("SLOPPA_URL", f.URL)
	t.Setenv("SLOPPA_TOKEN", f.token)
}

func (f *fakeAPI) requested(request string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.requests, request)
}

// stored is what the fake holds: its artifacts, newest first, and every upload's parts.
func (f *fakeAPI) stored() ([]api.Artifact, [][]part) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.artifacts), slices.Clone(f.parts)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, e *api.Error) { writeJSON(w, e.Status, e) }

func (f *fakeAPI) artifact(name, collection string, size int) api.Artifact {
	id := uuid.NewString()
	return api.Artifact{
		ID:          id,
		Name:        name,
		ContentType: "text/plain",
		Size:        int64(size),
		CreatedAt:   api.Time(time.Now()),
		Collection:  collection,
		URL:         f.URL + api.ViewerPath(id),
		RawURL:      f.URL + api.RawPath(id, name),
	}
}

// add stores artifacts as if uploaded, newest last.
func (f *fakeAPI) add(artifacts ...api.Artifact) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range artifacts {
		f.artifacts = append([]api.Artifact{a}, f.artifacts...)
	}
}

func (f *fakeAPI) upload(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, api.BadRequest(err.Error()))
		return
	}
	var parts []part
	for {
		p, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeError(w, api.BadRequest(err.Error()))
			return
		}
		data, _ := io.ReadAll(p)
		parts = append(parts, part{field: p.FormName(), filename: p.FileName(), data: data})
	}
	f.mu.Lock()
	f.parts = append(f.parts, parts)
	f.mu.Unlock()
	if len(parts) == 0 || parts[0].field != "collection" {
		writeError(w, api.BadRequest("the collection comes first"))
		return
	}
	var created []api.Artifact
	for _, p := range parts[1:] {
		created = append(created, f.artifact(p.filename, string(parts[0].data), len(p.data)))
	}
	f.add(created...)
	writeJSON(w, 200, created)
}

// list pages by offset, the cursor being the offset to go on from.
func (f *fakeAPI) list(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	var matched []api.Artifact
	for _, a := range f.artifacts {
		if q.Has("collection") && a.Collection != q.Get("collection") {
			continue
		}
		if q.Has("q") && !strings.Contains(strings.ToLower(a.Name), strings.ToLower(q.Get("q"))) {
			continue
		}
		matched = append(matched, a)
	}
	limit := api.PageSize
	if q.Has("limit") {
		limit, _ = strconv.Atoi(q.Get("limit"))
	}
	from, _ := strconv.Atoi(q.Get("cursor"))
	page := api.ArtifactPage{Items: matched[min(from, len(matched)):min(from+limit, len(matched))]}
	if from+limit < len(matched) {
		page.NextCursor = strconv.Itoa(from + limit)
	}
	writeJSON(w, 200, page)
}

func (f *fakeAPI) withArtifact(w http.ResponseWriter, r *http.Request, do func(*api.Artifact)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.artifacts {
		if f.artifacts[i].ID == r.PathValue("id") {
			do(&f.artifacts[i])
			return
		}
	}
	writeError(w, api.ArtifactNotFound(r.PathValue("id")))
}

func (f *fakeAPI) collections(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	summaries := []api.CollectionSummary{}
	for _, a := range f.artifacts {
		i := slices.IndexFunc(summaries, func(s api.CollectionSummary) bool { return s.Collection == a.Collection })
		if i < 0 {
			summaries = append(summaries, api.CollectionSummary{Collection: a.Collection, LastUploadAt: a.CreatedAt})
			i = len(summaries) - 1
		}
		summaries[i].Count++
	}
	writeJSON(w, 200, summaries)
}

func (f *fakeAPI) startPreview(w http.ResponseWriter, r *http.Request) {
	var in api.StartPreview
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, api.BadRequest(err.Error()))
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.previews {
		if in.Name != "" && p.Name == in.Name {
			writeError(w, api.PreviewNameTaken(in.Name))
			return
		}
	}
	id := fmt.Sprintf("prev%04d", len(f.previews)+1)
	label := id
	if in.Name != "" {
		label = in.Name
	}
	p := &fakePreview{
		Preview: api.Preview{
			ID:        id,
			URL:       "http://" + label + ".preview.localhost/",
			Name:      in.Name,
			Port:      in.Port,
			Command:   in.Command,
			ExpiresAt: api.Time(time.Now().Add(f.expiresIn)),
		},
		start:   in,
		tunnels: make(chan *tunnel.Server, 4),
	}
	f.previews[id] = p
	tunnelURL := "ws" + strings.TrimPrefix(f.URL, "http") + "/api/previews/" + id + "/tunnel"
	writeJSON(w, 200, api.StartedPreview{ID: id, URL: p.URL, TunnelURL: tunnelURL, ExpiresAt: p.ExpiresAt})
}

func (f *fakeAPI) preview(t *testing.T) *fakePreview {
	t.Helper()
	var found *fakePreview
	eventually(t, "a preview to start", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, p := range f.previews {
			found = p
		}
		return found != nil
	})
	return found
}

func (f *fakeAPI) tunnel(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	p := f.previews[r.PathValue("id")]
	refuse := f.refuseTunnels
	f.mu.Unlock()
	if p == nil {
		writeError(w, api.PreviewNotFound(r.PathValue("id")))
		return
	}
	if refuse != 0 {
		writeError(w, &api.Error{Status: refuse, Tag: "StorageError", Message: "not now"})
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{tunnel.Subprotocol}})
	if err != nil {
		return
	}
	if ws.Subprotocol() != tunnel.Subprotocol {
		ws.Close(tunnel.CloseProtocol, "speak "+tunnel.Subprotocol)
		return
	}
	srv, err := tunnel.NewServer(context.Background(), ws)
	if err != nil {
		return
	}
	p.tunnels <- srv
	<-srv.Done()
}

// next is the tunnel's next connection.
func (p *fakePreview) next(t *testing.T) *tunnel.Server {
	t.Helper()
	select {
	case srv := <-p.tunnels:
		return srv
	case <-time.After(15 * time.Second):
		t.Fatal("the tunnel didn't connect")
		return nil
	}
}

// get is a request through the tunnel to the dev server.
func get(t *testing.T, srv *tunnel.Server, path string) (*http.Response, []byte) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return srv.Dial(ctx) },
	}, Timeout: 10 * time.Second}
	resp, err := client.Get("http://preview" + path)
	if err != nil {
		t.Fatalf("GET %s through the tunnel: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}
