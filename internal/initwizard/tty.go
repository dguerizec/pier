package initwizard

import (
	"os"

	"github.com/mattn/go-isatty"
)

// IsInteractive reports whether stdin and stdout are both connected to
// a terminal. When either side is piped or redirected we treat the
// session as unattended (script, CI, AI agent) and skip prompts.
func IsInteractive() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}
