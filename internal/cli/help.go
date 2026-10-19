package cli

import (
	"fmt"
	"strings"

	"github.com/urfave/cli/v3"
)

type argument struct{ name, usage string }

type example struct{ command, description string }

// helpSections is the help a command has beyond its flags: what each argument is, and examples to
// copy, each under its description when it has one.
func helpSections(arguments []argument, examples ...example) map[string]any {
	var b strings.Builder
	if len(arguments) > 0 {
		width := 0
		for _, a := range arguments {
			width = max(width, len(a.name))
		}
		b.WriteString("ARGUMENTS:\n")
		for _, a := range arguments {
			fmt.Fprintf(&b, "   %-*s  %s\n", width, a.name, a.usage)
		}
	}
	if len(examples) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("EXAMPLES:\n")
		for i, e := range examples {
			if i > 0 {
				b.WriteString("\n")
			}
			if e.description != "" {
				fmt.Fprintf(&b, "   # %s\n", e.description)
			}
			fmt.Fprintf(&b, "   %s\n", e.command)
		}
	}
	return map[string]any{"help": b.String()}
}

// The help templates end with the sections helpSections makes, for the commands that have them.
func init() {
	const sections = `{{with .Metadata}}{{with index . "help"}}
{{.}}{{end}}{{end}}`
	cli.RootCommandHelpTemplate += sections
	cli.CommandHelpTemplate += sections
	cli.SubcommandHelpTemplate += sections
}
