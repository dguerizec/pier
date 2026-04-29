package initwizard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorktreeDirGitignoreEntry returns the .gitignore line to add for the
// configured worktree dir, or "" when no entry is needed:
//   - empty dir → user disabled the shorthand, nothing to ignore
//   - absolute path or path that resolves outside the repo → out of scope
//   - already covered by an ancestor we ignore (`.pier/`) → no-op
//
// Otherwise we return the dir relative to toplevel with a trailing slash
// so gitignore matches it as a directory rather than a file.
func WorktreeDirGitignoreEntry(toplevel, dir string) string {
	if dir == "" {
		return ""
	}
	// `~`-prefixed dirs always resolve outside the project (HOME), so
	// there's nothing to gitignore. Treat them as out of scope before
	// the IsAbs check, which doesn't recognise `~`.
	if strings.HasPrefix(dir, "~") && (len(dir) == 1 || dir[1] == '/') {
		return ""
	}
	abs := dir
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(toplevel, abs)
	}
	rel, err := filepath.Rel(toplevel, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	clean := filepath.ToSlash(rel)
	if clean == "" || clean == "." {
		return ""
	}
	if clean == ".pier" || strings.HasPrefix(clean, ".pier/") {
		// .pier/ already lives in the gitignore; adding `.pier/worktrees/`
		// would be noise.
		return ""
	}
	return clean + "/"
}

// EnsureGitignore appends entry to <toplevel>/.gitignore if not already there.
func EnsureGitignore(toplevel, entry string) error {
	path := filepath.Join(toplevel, ".gitignore")
	body, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(body) > 0 && !strings.HasSuffix(string(body), "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(f, entry)
	return err
}
