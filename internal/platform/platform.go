// Package platform holds the few OS-specific operations prowl needs:
// desktop notifications, opening URLs, the clipboard, process checks, and
// running `prowl watch` at login.
package platform

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
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

// Terminal is the terminal prowl draws on. A write to it finishes before the
// next starts, so the TUI's frames and Copy's OSC 52 can't run into each
// other. The TUI must draw through it.
var Terminal = &terminal{File: os.Stdout}

type terminal struct {
	*os.File
	mu sync.Mutex
}

func (t *terminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.File.Write(p)
}

// TerminalCmd runs a command on the terminal itself, for an editor or an
// agent's session started from the TUI. Bubble Tea hands a command the
// writer the TUI draws through, Terminal, and exec.Cmd puts a pipe between a
// command and a writer that isn't a file, so the editor would draw into it.
type TerminalCmd struct{ *exec.Cmd }

func (c TerminalCmd) SetStdin(r io.Reader)  { c.Stdin = r }
func (c TerminalCmd) SetStdout(io.Writer)   { c.Stdout = Terminal.File }
func (c TerminalCmd) SetStderr(w io.Writer) { c.Stderr = w }

// Copy puts text on the clipboard of the machine you are sitting at. It
// tries each tool the session can reach: wl-copy on Wayland, xclip or xsel
// on X11, PowerShell on Windows and WSL, pbcopy on macOS. When none works,
// as over SSH, it asks the terminal to take the text with OSC 52.
func Copy(text string) error {
	setClipboard := []string{"-NoProfile", "-NonInteractive", "-Command",
		"[Console]::InputEncoding = [Text.Encoding]::UTF8; Set-Clipboard -Value ([Console]::In.ReadToEnd())"}
	if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell", setClipboard...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("copy with PowerShell: %w", err)
		}
		return nil
	}
	var tools [][]string
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		tools = append(tools, []string{"wl-copy"})
	}
	if os.Getenv("DISPLAY") != "" {
		tools = append(tools, []string{"xclip", "-selection", "clipboard"}, []string{"xsel", "-ib"})
	}
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		tools = append(tools, append([]string{"powershell.exe"}, setClipboard...))
	}
	if runtime.GOOS == "darwin" && os.Getenv("SSH_TTY") == "" {
		tools = append(tools, []string{"pbcopy"})
	}
	for _, t := range tools {
		cmd := exec.Command(t[0], t[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if cmd.Run() == nil {
			return nil
		}
	}
	if !term.IsTerminal(int(Terminal.Fd())) {
		return errors.New("no clipboard to copy to: install wl-clipboard or xclip, or use a terminal that supports OSC 52")
	}
	_, err := fmt.Fprintf(Terminal, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
	return err
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
