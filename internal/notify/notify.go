// Package notify sends transient desktop notifications through the host OS.
// Delivery is best effort: failures are logged, never returned.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Notification helpers get this long beyond ttl before they are killed.
const grace = 10 * time.Second

// Upper bound on how long the Windows helper keeps its NotifyIcon alive.
const windowsBalloonHoldMax = 15 * time.Second

// Send shows a native toast with the given title and body. ttl is a hint for
// how long the toast should stay visible; not every platform honours it.
func Send(title, body string, ttl time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), ttl+grace)
	defer cancel()
	cmd := command(ctx, runtime.GOOS, oneLine(title), oneLine(body), ttl)
	if cmd == nil {
		slog.Debug("desktop notifications unsupported on this platform", "os", runtime.GOOS)
		return
	}
	if err := cmd.Run(); err != nil {
		slog.Warn("desktop notification failed", "error", err)
	}
}

func command(ctx context.Context, goos, title, body string, ttl time.Duration) *exec.Cmd {
	switch goos {
	case "linux", "freebsd", "openbsd", "netbsd":
		return exec.CommandContext(ctx, "notify-send",
			"--app-name=oh-shit-meeting",
			"--urgency=normal",
			fmt.Sprintf("--expire-time=%d", ttl.Milliseconds()),
			title, body)
	case "darwin":
		script := fmt.Sprintf("display notification %s with title %s",
			appleScriptString(body), appleScriptString(title))
		return exec.CommandContext(ctx, "osascript", "-e", script)
	case "windows":
		script := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms;`+
			`Add-Type -AssemblyName System.Drawing;`+
			`$n = New-Object System.Windows.Forms.NotifyIcon;`+
			`$n.Icon = [System.Drawing.SystemIcons]::Information;`+
			`$n.Visible = $true;`+
			`$n.ShowBalloonTip(%d, %s, %s, [System.Windows.Forms.ToolTipIcon]::Info);`+
			`Start-Sleep -Milliseconds %d; $n.Dispose()`,
			ttl.Milliseconds(), psString(title), psString(body), min(ttl, windowsBalloonHoldMax).Milliseconds())
		return exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	}
	return nil
}

// oneLine collapses line breaks so toasts stay single-paragraph.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func appleScriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func psString(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}
