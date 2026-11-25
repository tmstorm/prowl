package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
		if err := writeFile(path, plistFor(exe)); err != nil {
			return "", err
		}
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain, path).Run()
		if err := run("launchctl", "bootstrap", domain, path); err != nil {
			return "", err
		}
		return "launchd agent " + path, nil
	case "windows":
		if err := setRunKey(exe); err != nil {
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
	if err := writeFile(path, unitFor(exe)); err != nil {
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

// ErrOtherProwl is RefreshService's answer when the service runs another
// prowl binary than the one asking, such as a build from source run by hand.
var ErrOtherProwl = errors.New("the service runs another prowl")

// ErrEdited is RefreshService's answer when the service was changed since
// prowl last wrote it, by hand or by another prowl.
var ErrEdited = errors.New("the service was edited since prowl wrote it")

// ServiceSum identifies a service definition, so prowl can tell later
// whether the one installed is still what it wrote.
func ServiceSum(exe string) string { return sum(serviceFor(exe)) }

// RefreshService rewrites the installed service in the form this prowl
// writes for exe, when it is what prowl last wrote (last, from ServiceSum)
// and differs. It restarts nothing: the prowl asking already runs the new
// binary. It returns the sum of the service prowl wrote that is installed
// now, for the next refresh, and whether it rewrote it. Nothing installed is
// nothing to do; one that runs another binary is ErrOtherProwl, and one
// edited since is ErrEdited. On Windows it first moves prowl 0.1.0's logon
// task to the startup entry, when it may delete the task; otherwise the task
// stays, so prowl watch never starts twice at login.
func RefreshService(exe, last string) (string, bool, error) {
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("schtasks", "/Query", "/TN", serviceName, "/XML").Output(); err == nil {
			if !strings.Contains(string(out), exe+"'") {
				return last, false, ErrOtherProwl
			}
			if err := run("schtasks", "/Delete", "/F", "/TN", serviceName); err != nil {
				return last, false, fmt.Errorf("the logon task prowl 0.1.0 made stays, since it can't be deleted without admin: %w", err)
			}
			if err := setRunKey(exe); err != nil {
				return last, true, fmt.Errorf("deleted the logon task prowl 0.1.0 made, but couldn't add the startup entry; prowl service install adds it: %w", err)
			}
			return ServiceSum(exe), true, nil
		}
	}
	got, want := installedService(), serviceFor(exe)
	switch {
	case got == "":
		return last, false, nil
	case got == want:
		return sum(want), false, nil
	case !strings.Contains(got, serviceNames(exe)):
		return last, false, ErrOtherProwl
	case sum(got) != last:
		return last, false, ErrEdited
	}
	var err error
	switch runtime.GOOS {
	case "darwin":
		err = writeFile(launchdPlist(), want)
	case "windows":
		err = setRunKey(exe)
	default:
		if err = writeFile(systemdUnit(), want); err == nil {
			return sum(want), true, run("systemctl", "--user", "daemon-reload")
		}
	}
	if err != nil {
		return last, false, err
	}
	return sum(want), true, nil
}

// installedService is the service definition installed, or "": the unit
// or plist, or the startup entry's command.
func installedService() string {
	switch runtime.GOOS {
	case "darwin":
		b, _ := os.ReadFile(launchdPlist())
		return string(b)
	case "windows":
		out, _ := exec.Command("reg", "query", runKey, "/v", serviceName).Output()
		_, cmd, _ := strings.Cut(string(out), "REG_SZ")
		return strings.TrimSpace(cmd)
	}
	b, _ := os.ReadFile(systemdUnit())
	return string(b)
}

// serviceFor is the service definition this prowl writes for exe.
func serviceFor(exe string) string {
	switch runtime.GOOS {
	case "darwin":
		return plistFor(exe)
	case "windows":
		return runCommand(exe)
	}
	return unitFor(exe)
}

// serviceNames is how a service definition spells exe.
func serviceNames(exe string) string {
	switch runtime.GOOS {
	case "darwin":
		return "<string>" + html.EscapeString(exe) + "</string>"
	case "windows":
		return runQuote(exe)
	}
	return systemdQuote(exe)
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func unitFor(exe string) string { return fmt.Sprintf(unit, systemdQuote(exe)) }

func plistFor(exe string) string { return fmt.Sprintf(plist, serviceName, html.EscapeString(exe)) }

// runCommand is the startup entry's command, which runs exe watch with no
// window.
func runCommand(exe string) string {
	return fmt.Sprintf(`powershell.exe -NoProfile -WindowStyle Hidden -Command "& %s watch"`, runQuote(exe))
}

// runQuote quotes a path for PowerShell.
func runQuote(exe string) string { return "'" + strings.ReplaceAll(exe, "'", "''") + "'" }

func setRunKey(exe string) error {
	return run("reg", "add", runKey, "/v", serviceName, "/t", "REG_SZ", "/d", runCommand(exe), "/f")
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
