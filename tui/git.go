package tui

import (
	"os/exec"
	"strings"
)

// gitDiffStat returns a short diff stat summary for the given directory,
// or an empty string if the directory is not a git repo or has no changes.
func gitDiffStat(dir string) string {
	// Check if this is a git repo
	check := exec.Command("git", "-C", dir, "rev-parse", "--git-dir")
	if err := check.Run(); err != nil {
		return ""
	}

	// Get diff stat for uncommitted changes
	cmd := exec.Command("git", "-C", dir, "diff", "--stat", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	lines := strings.TrimSpace(string(out))
	if lines == "" {
		return ""
	}

	// Return the last line which is the summary (e.g. "3 files changed, 45 insertions(+), 12 deletions(-)")
	parts := strings.Split(lines, "\n")
	return strings.TrimSpace(parts[len(parts)-1])
}
