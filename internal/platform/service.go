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

// InstallService has `exe watch` start at login, and starts it now: a
// systemd user service on Linux, a launchd agent on macOS, a Task Scheduler
// task on Windows. It returns what it set up, for people.
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
		if err := run("schtasks", "/Create", "/F", "/TN", serviceName, "/SC", "ONLOGON", "/RL", "LIMITED", "/TR", action); err != nil {
			return "", err
		}
		if err := run("schtasks", "/Run", "/TN", serviceName); err != nil {
			return "", err
		}
		return "Task Scheduler task " + serviceName + ", run at logon", nil
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
// InstallService set up.
func UninstallService() error {
	switch runtime.GOOS {
	case "darwin":
		path := launchdPlist()
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), path).Run()
		return removeFile(path)
	case "windows":
		_ = exec.Command("schtasks", "/End", "/TN", serviceName).Run()
		return run("schtasks", "/Delete", "/F", "/TN", serviceName)
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
		return exec.Command("schtasks", "/Query", "/TN", serviceName).Run() == nil
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
