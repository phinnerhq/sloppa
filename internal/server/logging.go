package server

import (
	"log/slog"
	"os"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
)

// SetupLogging logs in color to a terminal, and as JSON lines anywhere else (a container's log).
func SetupLogging() {
	var handler slog.Handler
	if isatty.IsTerminal(os.Stderr.Fd()) {
		handler = tint.NewHandler(os.Stderr, &tint.Options{TimeFormat: "15:04:05.000", NoColor: os.Getenv("NO_COLOR") != ""})
	} else {
		handler = slog.NewJSONHandler(os.Stderr, nil)
	}
	slog.SetDefault(slog.New(handler))
}
