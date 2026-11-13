package platform

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// serviceName names the background `prowl watch` in each OS's service list.
const serviceName = "prowl-watch"

// runKey is the Windows registry key of programs that start at your login.
// Unlike a logon task in Task Scheduler, adding to it needs no admin.
const runKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`

// InstallService has `exe watch` start at login, and starts it now: a
// systemd user service on Linux, a launchd agent on macOS, a startup entry on
// Windows. It returns what it set up, for people.
func InstallService(exe string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		path := launchdPlist()
		if err := writeFile(path, fmt.Sprintf(plist, serviceName, html.EscapeString(exe))); err != nil {
			return "", err
		}
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain, path).Run()
		if err := run("launchctl", "bootstrap", domain, path); err != nil {
			return "", err
		}
		return "launchd agent " + path, nil
	case "windows":
		action := fmt.Sprintf(`powershell.exe -NoProfile -WindowStyle Hidden -Command "& '%s' watch"`, strings.ReplaceAll(exe, "'", "''"))
		if err := run("reg", "add", runKey, "/v", serviceName, "/t", "REG_SZ", "/d", action, "/f"); err != nil {
			return "", err
		}
		cmd := exec.Command(exe, "watch")
		Detach(cmd)
		if err := cmd.Start(); err != nil {
			return "", fmt.Errorf("start prowl watch: %w", err)
		}
		return "startup entry " + serviceName + " in " + runKey, nil
	}
	path := systemdUnit()
	if err := writeFile(path, fmt.Sprintf(unit, systemdQuote(exe))); err != nil {
		return "", err
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return "", err
	}
	if err := run("systemctl", "--user", "enable", "--now", serviceName+".service"); err != nil {
		return "", err
	}
	return "systemd user service " + path, nil
}

// UninstallService stops the background `prowl watch` and removes what
// InstallService set up. On Windows that includes the logon task prowl 0.1.0
// made, where an admin could make one.
func UninstallService() error {
	switch runtime.GOOS {
	case "darwin":
		path := launchdPlist()
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), path).Run()
		return removeFile(path)
	case "windows":
		_ = exec.Command("powershell", "-NoProfile", "-Command",
			`Get-CimInstance Win32_Process | Where-Object { $_.Name -eq 'prowl.exe' -and $_.CommandLine -like '* watch*' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }`).Run()
		_ = exec.Command("schtasks", "/Delete", "/F", "/TN", serviceName).Run()
		return run("reg", "delete", runKey, "/v", serviceName, "/f")
	}
	_ = exec.Command("systemctl", "--user", "disable", "--now", serviceName+".service").Run()
	if err := removeFile(systemdUnit()); err != nil {
		return err
	}
	return run("systemctl", "--user", "daemon-reload")
}

// systemdQuote quotes a path for ExecStart, which splits on spaces and
// expands % specifiers.
func systemdQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(s) + `"`
}

const unit = `[Unit]
Description=prowl PR review notifications

[Service]
ExecStart=%s watch
Restart=on-failure
RestartSec=30

[Install]
WantedBy=default.target
`

const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>watch</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
</dict>
</plist>
`

// ServiceInstalled reports whether InstallService set up `prowl watch` to
// run at login.
func ServiceInstalled() bool {
	switch runtime.GOOS {
	case "darwin":
		_, err := os.Stat(launchdPlist())
		return err == nil
	case "windows":
		return exec.Command("reg", "query", runKey, "/v", serviceName).Run() == nil
	}
	_, err := os.Stat(systemdUnit())
	return err == nil
}

func systemdUnit() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", serviceName+".service")
}

func launchdPlist() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", serviceName+".plist")
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	return nil
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("service: %w", err)
	}
	return nil
}

func run(name string, args ...string) error {
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
