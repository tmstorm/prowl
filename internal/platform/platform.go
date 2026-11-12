// Package platform holds the few OS-specific operations prowl needs:
// desktop notifications, opening URLs, process checks, and running
// `prowl watch` at login.
package platform

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

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

// Open opens a URL in the default browser.
func Open(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
	return exec.Command("xdg-open", url).Start()
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
