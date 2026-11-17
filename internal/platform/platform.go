// Package platform holds the few OS-specific operations prowl needs:
// desktop notifications, opening URLs, process checks, and running
// `prowl watch` at login.
package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ErrLocked is TryLock's answer while another process holds the lock.
var ErrLocked = errors.New("locked by another process")

// Notify shows a desktop notification that expires after timeout, where the
// OS supports it. It fails silently: notifications are best effort. Linux
// notifications use normal urgency, since daemons keep critical ones up until
// dismissed.
func Notify(title, body string, timeout time.Duration) {
	switch runtime.GOOS {
	case "darwin":
		esc := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) }
		_ = exec.Command("osascript", "-e",
			fmt.Sprintf(`display notification "%s" with title "prowl" subtitle "%s"`, esc(body), esc(title))).Run()
	case "windows":
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", windowsToast)
		cmd.Env = append(os.Environ(), "PROWL_TITLE="+title, "PROWL_BODY="+body, fmt.Sprintf("PROWL_SECONDS=%d", int(timeout.Seconds())))
		_ = cmd.Run()
	default:
		_ = exec.Command("notify-send", "-a", "prowl", "-u", "normal",
			"-t", fmt.Sprint(timeout.Milliseconds()), title, body).Run()
	}
}

// SlackApp reports whether the Slack desktop app is installed to open
// slack:// links, so a message can open there instead of in the browser.
func SlackApp() bool {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", "-Ra", "Slack").Run() == nil
	case "windows":
		return exec.Command("reg", "query", `HKCR\slack`).Run() == nil
	}
	out, err := exec.Command("xdg-mime", "query", "default", "x-scheme-handler/slack").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// Open opens a URL in the default browser, or another app's link (such as
// slack://) in that app. On Linux such a link goes through gio when it's
// there: KDE's xdg-open turns slack://channel into slack:///channel, which
// Slack doesn't follow.
func Open(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return Spawn(exec.Command("open", url))
	case "windows":
		return Spawn(exec.Command("rundll32", "url.dll,FileProtocolHandler", url))
	}
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		if _, err := exec.LookPath("gio"); err == nil {
			return Spawn(exec.Command("gio", "open", url))
		}
	}
	return Spawn(exec.Command("xdg-open", url))
}

// Spawn starts cmd without waiting for it, and reaps it once it exits, so
// it doesn't stay behind as a zombie for as long as prowl runs.
func Spawn(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// windowsToast shows a Windows toast with the built-in notification API,
// as PowerShell. The text comes from the environment, so nothing in it
// needs quoting.
const windowsToast = `
$m = [Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime]
$xml = $m::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$text = $xml.GetElementsByTagName('text')
$text.Item(0).AppendChild($xml.CreateTextNode($env:PROWL_TITLE)) > $null
$text.Item(1).AppendChild($xml.CreateTextNode($env:PROWL_BODY)) > $null
$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)
$toast.ExpirationTime = [DateTimeOffset]::Now.AddSeconds([int]$env:PROWL_SECONDS)
$m::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show($toast)
`
