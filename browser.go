package main

import (
	"os/exec"
	"runtime"
	"time"
)

// openInBrowser opens the app once the server is up, so double-clicking the
// binary is all a new user has to do.
func openInBrowser(u string) {
	time.Sleep(400 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}
