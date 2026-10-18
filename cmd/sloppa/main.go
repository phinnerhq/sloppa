// Command sloppa is both the server (`sloppa serve`) and the CLI agents use to upload artifacts and
// share dev servers.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/urfave/cli/v3"

	sloppacli "github.com/phinnerhq/sloppa/internal/cli"
	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/server"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

func serve(ctx context.Context, _ *cli.Command) error {
	server.SetupLogging()
	// A .env in the working directory, for development; the environment wins over it.
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf(".env: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	return server.Run(ctx, cfg)
}

// healthcheck is the container's: the image has no shell or curl to ask the server with.
func healthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "4100"
	}
	client := http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/api/openapi.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, resp.Status)
		return 1
	}
	return 0
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	// Errors are reported and exit with their code inside App.
	sloppacli.App(serve, version).Run(context.Background(), os.Args)
}
