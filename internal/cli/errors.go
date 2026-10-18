package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/phinnerhq/sloppa/internal/api"
)

// userError is a failure whose message says what to do about it, printed as it is.
type userError struct {
	message string
	cause   error
}

func (e *userError) Error() string { return e.message }
func (e *userError) Unwrap() error { return e.cause }

func userErrorf(format string, args ...any) error {
	return &userError{message: fmt.Sprintf(format, args...)}
}

// exitError ends the command with a code of its own, such as the dev server's, having said
// whatever there was to say already.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

const signInAgain = `printf %s "$TOKEN" | sloppa login <url>`

// apiFailure says what failed, then the server's own message for it, which says what to do about it.
// A message of ours stays as it is.
func apiFailure(what string, err error) error {
	var ue *userError
	if errors.As(err, &ue) {
		return err
	}
	var ae *api.Error
	if errors.As(err, &ae) && (ae.Tag == "Unauthorized" || ae.Tag == "" && ae.Status == 401) {
		return &userError{
			message: fmt.Sprintf("%s: the server refused this agent token. Sign in again:\n  %s", what, signInAgain),
			cause:   err,
		}
	}
	return &userError{message: fmt.Sprintf("%s: %v", what, err), cause: err}
}

// isTag tells whether err is the API error tagged tag, like ArtifactNotFound.
func isTag(err error, tag string) bool {
	var ae *api.Error
	return errors.As(err, &ae) && ae.Tag == tag
}

// report prints what went wrong, if anything is left to say, and returns the code to exit with:
// 130 for an interrupted command, like a shell gives Ctrl-C.
func report(w io.Writer, err error) int {
	var exit *exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.code
	case errors.Is(err, context.Canceled):
		return 130
	}
	fmt.Fprintln(w, err)
	return 1
}
