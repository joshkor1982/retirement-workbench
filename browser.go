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

// openFolder shows a folder in Finder, File Explorer, or the Linux file
// manager. It only ever opens the app's own data folder.
func openFolder(dir string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	return cmd.Start()
}

// folderApp names the file manager for the button label.
func folderApp() string {
	switch runtime.GOOS {
	case "darwin":
		return "Finder"
	case "windows":
		return "File Explorer"
	}
	return "Files"
}
