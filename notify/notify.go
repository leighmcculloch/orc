package notify

import (
	"os/exec"
	"runtime"
	"strings"
)

// Send sends a desktop notification. Fails silently if not supported.
func Send(title, body string) {
	switch runtime.GOOS {
	case "darwin":
		// Escape double quotes to prevent AppleScript injection
		safeTitle := strings.ReplaceAll(title, `"`, `\"`)
		safeBody := strings.ReplaceAll(body, `"`, `\"`)
		exec.Command("osascript", "-e", `display notification "`+safeBody+`" with title "`+safeTitle+`"`).Run()
	case "linux":
		exec.Command("notify-send", title, body).Run()
	}
}
