package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/schollz/progressbar/v3"
	"github.com/urfave/cli/v3"

	"github.com/phinnerhq/sloppa/internal/api"
)

// upload is one file to send, opened already.
type upload struct {
	name string
	size int64
	body io.Reader
	// file is closed once sent, when body is one.
	file *os.File
}

func (u upload) close() {
	if u.file != nil {
		u.file.Close()
	}
}

func closeUploads(uploads []upload) {
	for _, u := range uploads {
		u.close()
	}
}

var tooBig = humanize.IBytes(api.MaxUploadBytes)

// openUpload opens what to send for one argument: a file, or stdin for `-`, which is read whole.
func openUpload(ctx context.Context, file, name string, stdin io.Reader) (upload, error) {
	if file == "-" {
		if name == "" {
			return upload{}, userErrorf("Name what stdin holds: … | sloppa artifact upload -c <collection> --name report.md -")
		}
		if isTerminal(stdin) {
			return upload{}, userErrorf("Nothing is piped in for -: … | sloppa artifact upload -c <collection> --name %s -", name)
		}
		data, err := readStdin(ctx, stdin, api.MaxUploadBytes+1)
		if err != nil {
			return upload{}, fmt.Errorf("reading stdin: %w", err)
		}
		if len(data) > api.MaxUploadBytes {
			return upload{}, userErrorf("stdin holds more than %s, the most an artifact can be", tooBig)
		}
		return upload{name: name, size: int64(len(data)), body: bytes.NewReader(data)}, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return upload{}, userErrorf("Not a file: %s", file)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return upload{}, userErrorf("Not a file: %s", file)
	}
	if info.Size() > api.MaxUploadBytes {
		f.Close()
		return upload{}, userErrorf("%s is %s, more than the %s an artifact can be", file, humanize.IBytes(uint64(info.Size())), tooBig)
	}
	return upload{name: filepath.Base(file), size: info.Size(), body: f, file: f}, nil
}

var imageName = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|webp|avif|svg)$`)

// markdownLink is ready to paste into a PR: images embed, anything else links.
func markdownLink(a api.Artifact) string {
	link := fmt.Sprintf("[%s](%s)", a.Name, a.RawURL)
	if imageName.MatchString(a.Name) {
		return "!" + link
	}
	return link
}

func (a *app) uploadCommand() *cli.Command {
	return &cli.Command{
		Name:      "upload",
		Usage:     "Upload files as artifacts and print a Markdown link to each, ready for a PR: images embed, anything else links.",
		ArgsUsage: "<files>...",
		Flags: []cli.Flag{
			collectionFlag(collectionUsage),
			&cli.StringFlag{Name: "name", Usage: "The file name for stdin, with extension"},
			jsonFlag(),
		},
		Metadata: helpSections(
			[]argument{{"files", "Files to upload; - reads stdin, named by --name"}},
			example{`sloppa artifact upload -c "owner/repo #42" checkout.png run.log`, "Two files for a PR"},
			example{`pnpm test 2>&1 | sloppa artifact upload -c "owner/repo #42" --name test-run.log -`, "Output piped in, under a name of your choosing"},
			example{`sloppa artifact upload -c "owner/repo #42" --json trace.zip`, "Ids and links as JSON"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			files := cmd.Args().Slice()
			if len(files) == 0 {
				return userErrorf("Name the files to upload, or - for stdin:\n  sloppa artifact upload -c \"owner/repo #42\" checkout.png")
			}
			collection, err := collectionOf(cmd)
			if err != nil {
				return err
			}
			stdins := 0
			for _, file := range files {
				if file == "-" {
					stdins++
				}
			}
			if stdins > 1 {
				return userErrorf("Stdin can be read once: pass - at most once")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			// Every file is opened before anything is sent, so a typo uploads nothing.
			uploads := make([]upload, 0, len(files))
			defer func() { closeUploads(uploads) }()
			for _, file := range files {
				u, err := openUpload(ctx, file, cmd.String("name"), cmd.Root().Reader)
				if err != nil {
					return err
				}
				uploads = append(uploads, u)
			}
			var progress io.Writer
			if !cmd.Bool("json") && isTerminal(stderr(cmd)) {
				bar := progressBar(stderr(cmd), uploads)
				defer bar.Exit()
				progress = bar
			}
			var artifacts []api.Artifact
			raw, err := c.upload(ctx, collection, uploads, progress, &artifacts)
			if err != nil {
				return apiFailure("Upload failed", err)
			}
			if cmd.Bool("json") {
				return printJSON(stdout(cmd), raw)
			}
			for _, artifact := range artifacts {
				fmt.Fprintln(stdout(cmd), markdownLink(artifact))
			}
			return nil
		},
	}
}

// progressBar counts the bytes sent, on a terminal; colourless when NO_COLOR asks.
func progressBar(w io.Writer, uploads []upload) *progressbar.ProgressBar {
	var total int64
	for _, u := range uploads {
		total += u.size
	}
	color := os.Getenv("NO_COLOR") == ""
	description := "Uploading"
	if color {
		description = "[cyan]Uploading[reset]"
	}
	return progressbar.NewOptions64(total,
		progressbar.OptionSetWriter(w),
		progressbar.OptionSetDescription(description),
		progressbar.OptionEnableColorCodes(color),
		progressbar.OptionShowBytes(true),
		progressbar.OptionShowCount(),
		progressbar.OptionShowTotalBytes(true),
		progressbar.OptionSetWidth(30),
		progressbar.OptionThrottle(100*time.Millisecond),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionClearOnFinish(),
	)
}

// upload streams the files as one multipart form: the collection first, so the server knows where
// they go before the first byte of them, then each under `files`.
func (c *client) upload(ctx context.Context, collection string, uploads []upload, progress io.Writer, out any) (json.RawMessage, error) {
	body, w := io.Pipe()
	form := multipart.NewWriter(w)
	written := make(chan error, 1)
	go func() {
		err := writeForm(form, collection, uploads, progress)
		w.CloseWithError(err)
		written <- err
	}()
	raw, err := c.send(ctx, http.MethodPost, "/api/artifacts", nil, body, form.FormDataContentType(), out)
	// The server may answer before reading everything, refusing it.
	body.CloseWithError(io.ErrClosedPipe)
	if werr := <-written; werr != nil && !errors.Is(werr, io.ErrClosedPipe) && err != nil {
		return nil, werr
	}
	return raw, err
}

func writeForm(form *multipart.Writer, collection string, uploads []upload, progress io.Writer) error {
	if err := form.WriteField("collection", collection); err != nil {
		return err
	}
	for _, u := range uploads {
		part, err := form.CreateFormFile("files", u.name)
		if err != nil {
			return err
		}
		var src io.Reader = u.body
		if progress != nil {
			src = io.TeeReader(src, progress)
		}
		if _, err := io.Copy(part, src); err != nil {
			if errors.Is(err, io.ErrClosedPipe) {
				return err
			}
			return fmt.Errorf("reading %s: %w", u.name, err)
		}
	}
	return form.Close()
}
