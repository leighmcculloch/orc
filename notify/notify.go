package notify

import (
	"os/exec"
	"runtime"
)

// Send sends a desktop notification. Fails silently if not supported.
func Send(title, body string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("osascript", "-e", `display notification "`+body+`" with title "`+title+`"`).Run()
	case "linux":
		exec.Command("notify-send", title, body).Run()
	}
}
