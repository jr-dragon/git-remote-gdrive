// Package browser opens URLs in the system's default browser.
package browser

import (
	"fmt"
	"os/exec"
	"runtime"
)

func Open(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return fmt.Errorf("no browser launcher for %s", runtime.GOOS)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Some launchers remain alive for the lifetime of the browser.
	go func() { _ = cmd.Wait() }()
	return nil
}
