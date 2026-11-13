package core

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/jedisct1/go-minisign"
	"golang.org/x/mod/semver"

	"github.com/tmstorm/prowl/internal/store"
)

// Version is prowl's version, set by release builds.
var Version = ""

// ReleaseKey is the minisign public key (minisign.pub) that signs each
// release's checksums.txt. prowl installs nothing it can't verify against it.
var ReleaseKey = "RWRnTAuJsbzZwZRdbgiOGGmM7bLvnCgaIWYeDceGszaD6LAxs4DJdLCW"

const (
	releaseRepo = "tmstorm/prowl"
	checkEvery  = 24 * time.Hour
	// retryEvery spaces out background installs that failed: a broken
	// archive or a binary prowl can't replace would otherwise download the
	// release again on every poll.
	retryEvery = time.Hour
)

// errUnverified is a release download that doesn't match its signed
// checksums. Downloading it again won't help, unlike a network error.
var errUnverified = errors.New("unverified release")

// Executable is the running prowl's path, with symlinks resolved.
func Executable() string {
	exe, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real
	}
	return exe
}

// BuildVersion is the running version: the release's, what `go install`
// recorded, or "dev" for a build from source.
func BuildVersion() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// releaseVersion is the running version as a tag, "v0.1.0", for a release
// build, and "" for anything else: a build from source, a snapshot or a
// pseudo-version, which never check for updates.
func releaseVersion() string {
	v := "v" + strings.TrimPrefix(BuildVersion(), "v")
	if !semver.IsValid(v) || semver.Prerelease(v) != "" {
		return ""
	}
	return v
}

// Release is what prowl knows about its own updates.
type Release struct {
	Running   string // this prowl's version, "v0.1.0"; "" for a build from source
	Latest    string // the newest release, once prowl has asked
	Notes     string // the newest release's notes
	URL       string // its page on GitHub
	Installed bool   // prowl installed Latest itself; a restart runs it
	Command   string // what updates prowl when it can't do it itself; "" when it can
	Breaking  bool   // the newest release's notes list breaking changes
}

// Newer reports whether a newer release than the running one is out.
func (r Release) Newer() bool {
	return r.Running != "" && semver.IsValid(r.Latest) && semver.Compare(r.Latest, r.Running) > 0
}

// Release is what prowl knows about its updates, for the prowl at exe. It
// asks nothing of GitHub; CheckRelease does.
func (co *Core) Release(exe string) Release {
	last, _ := co.st.Release()
	r := Release{Running: releaseVersion(), Latest: last.Tag, Notes: last.Notes, URL: last.URL,
		Breaking: strings.Contains(last.Notes, "BREAKING CHANGES")}
	if r.Running != "" {
		r.Installed = semver.IsValid(last.Installed) && semver.Compare(last.Installed, r.Running) > 0
		r.Command = updateCommand(exe)
	}
	return r
}

// updateCommand is what updates the prowl at exe, or "" when prowl owns its
// binary and can replace it: a package manager's binary is never touched.
func updateCommand(exe string) string {
	if Version == "" {
		return "go install github.com/" + releaseRepo + "@latest"
	}
	return map[string]string{
		"brew":     "brew upgrade prowl",
		"scoop":    "scoop update prowl",
		"aur":      "yay -S prowl-bin",
		"readonly": "download it from https://github.com/" + releaseRepo + "/releases",
	}[owner(exe)]
}

// owner is what owns the prowl binary at exe: a package manager ("brew",
// "scoop", "aur"), "readonly" for a folder prowl can't write, or "" when
// prowl may replace or remove it.
func owner(exe string) string {
	p := filepath.ToSlash(exe)
	switch {
	case strings.Contains(p, "/Cellar/") || strings.HasPrefix(p, "/opt/homebrew/"):
		return "brew"
	case strings.Contains(strings.ToLower(p), "/scoop/"):
		return "scoop"
	case runtime.GOOS == "linux" && strings.HasPrefix(p, "/usr/") && !strings.HasPrefix(p, "/usr/local/"):
		return "aur"
	case !writable(filepath.Dir(exe)):
		return "readonly"
	}
	return ""
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".prowl-check-*")
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	return true
}

// CheckRelease asks GitHub for prowl's newest release and remembers it. It
// asks at most once a day, unless force, and never for a build from source
// or with check_updates off.
func (co *Core) CheckRelease(force bool) error {
	if releaseVersion() == "" || (!force && !co.Config().CheckUpdates) {
		return nil
	}
	if last, _ := co.st.Release(); !force && time.Since(last.Checked) < checkEvery {
		return nil
	}
	if co.gh == nil {
		return errors.New("check for updates: not logged in to GitHub")
	}
	r, err := co.gh.LatestRelease(releaseRepo)
	if err != nil {
		return err
	}
	return co.st.SetRelease(store.Release{Checked: time.Now(), Tag: r.Tag, Notes: r.Notes, URL: r.URL})
}

// Upgrade replaces the prowl at exe with the newest release and returns its
// version, or "" when exe is already the newest. The archive must match
// checksums.txt, and checksums.txt must carry a valid signature from
// ReleaseKey; otherwise exe is left as it was.
func (co *Core) Upgrade(exe string) (string, error) {
	running := releaseVersion()
	if running == "" {
		return "", errors.New("this prowl was built from source; update it with git pull and go build")
	}
	if cmd := updateCommand(exe); cmd != "" {
		return "", fmt.Errorf("prowl at %s isn't updated by prowl: %s", exe, cmd)
	}
	if co.gh == nil {
		return "", errors.New("update: not logged in to GitHub")
	}
	co.setUpdating("checking for a newer prowl")
	defer co.setUpdating("")
	r, err := co.gh.LatestRelease(releaseRepo)
	if err != nil {
		return "", err
	}
	_ = co.st.SetRelease(store.Release{Checked: time.Now(), Tag: r.Tag, Notes: r.Notes, URL: r.URL})
	last, _ := co.st.Release()
	have := running
	if semver.IsValid(last.Installed) && semver.Compare(last.Installed, have) > 0 {
		have = last.Installed
	}
	if !semver.IsValid(r.Tag) || semver.Compare(r.Tag, have) <= 0 {
		return "", nil
	}
	name, bin := archiveName(), "prowl"
	if runtime.GOOS == "windows" {
		bin = "prowl.exe"
	}
	co.setUpdating("verifying prowl " + r.Tag)
	sums, err := co.gh.Download(releaseRepo, r, "checksums.txt")
	if err != nil {
		return "", err
	}
	sig, err := co.gh.Download(releaseRepo, r, "checksums.txt.minisig")
	if err != nil {
		return "", err
	}
	if err := verifySums(sums, sig, r.Tag); err != nil {
		return "", fmt.Errorf("update to %s: %w: %w", r.Tag, errUnverified, err)
	}
	co.setUpdating("downloading prowl " + r.Tag)
	archive, err := co.gh.Download(releaseRepo, r, name)
	if err != nil {
		return "", err
	}
	if err := checkSum(sums, name, archive); err != nil {
		return "", fmt.Errorf("update to %s: %w: %w", r.Tag, errUnverified, err)
	}
	co.setUpdating("installing prowl " + r.Tag)
	b, err := unpack(archive, bin)
	if err != nil {
		return "", fmt.Errorf("update to %s: %w", r.Tag, err)
	}
	if err := replace(exe, b); err != nil {
		return "", fmt.Errorf("update to %s: %w", r.Tag, err)
	}
	_ = co.st.SetInstalled(r.Tag)
	return r.Tag, nil
}

// AutoUpgrade checks for a release once a day and, with auto_update on and
// prowl owning exe, installs it. A release that fails verification isn't
// downloaded again (`prowl update` tries again); any other failure is tried
// again an hour later. It returns the version it installed, or "".
func (co *Core) AutoUpgrade(exe string) (string, error) {
	if err := co.CheckRelease(false); err != nil {
		return "", err
	}
	cfg, r := co.Config(), co.Release(exe)
	if !cfg.CheckUpdates || !cfg.AutoUpdate || r.Command != "" || !r.Newer() || r.Installed {
		return "", nil
	}
	if last, _ := co.st.Release(); last.Attempted == r.Latest {
		return "", nil
	}
	co.mu.Lock()
	wait := time.Now().Before(co.updateRetry)
	co.mu.Unlock()
	if wait {
		return "", nil
	}
	v, err := co.Upgrade(exe)
	switch {
	case errors.Is(err, errUnverified):
		_ = co.st.SetAttempted(r.Latest)
	case err != nil:
		co.mu.Lock()
		co.updateRetry = time.Now().Add(retryEvery)
		co.mu.Unlock()
	}
	return v, err
}

// Updating is what a running update is doing, like "downloading prowl v0.2.0",
// or "" when none runs.
func (co *Core) Updating() string {
	co.mu.Lock()
	defer co.mu.Unlock()
	return co.updateStep
}

func (co *Core) setUpdating(step string) {
	co.mu.Lock()
	co.updateStep = step
	co.mu.Unlock()
	co.emit(Event{Kind: ReleaseChanged})
}

// WhatsNew is the running release and its notes, the first time this
// version asks once prowl knows them.
func (co *Core) WhatsNew() (string, string) {
	last, _ := co.st.Release()
	v := releaseVersion()
	if v == "" || last.Tag != v || strings.TrimSpace(last.Notes) == "" || !co.st.FirstTime("whatsnew "+v) {
		return "", ""
	}
	return v, last.Notes
}

// FirstNews reports whether prowl hasn't yet told you that release tag is
// out, and remembers that it now has.
func (co *Core) FirstNews(tag string) bool { return co.st.FirstTime("release " + tag) }

// OldBinary is where an update on Windows moves the running prowl.exe,
// which Windows won't let anyone overwrite. The next start removes it.
func OldBinary(exe string) string { return strings.TrimSuffix(exe, ".exe") + ".old.exe" }

func archiveName() string {
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("prowl_%s_%s.%s", runtime.GOOS, runtime.GOARCH, ext)
}

// verifySums checks that checksums.txt was signed with ReleaseKey for this
// release: the signature's trusted comment names the tag, so an older
// release's signed files can't pass as a newer one.
func verifySums(sums, sig []byte, tag string) error {
	key, err := minisign.NewPublicKey(ReleaseKey)
	if err != nil {
		return fmt.Errorf("release key: %w", err)
	}
	s, err := minisign.DecodeSignature(string(sig))
	if err != nil {
		return fmt.Errorf("checksums.txt.minisig: %w", err)
	}
	if ok, err := key.Verify(sums, s); !ok {
		return fmt.Errorf("checksums.txt isn't signed by prowl's release key: %w", err)
	}
	if s.TrustedComment != "trusted comment: prowl "+tag {
		return fmt.Errorf("checksums.txt was signed for another release (%s)", strings.TrimPrefix(s.TrustedComment, "trusted comment: "))
	}
	return nil
}

func checkSum(sums []byte, name string, archive []byte) error {
	got := sha256.Sum256(archive)
	for l := range strings.SplitSeq(string(sums), "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[1] == name {
			if f[0] != hex.EncodeToString(got[:]) {
				return fmt.Errorf("%s doesn't match checksums.txt", name)
			}
			return nil
		}
	}
	return fmt.Errorf("checksums.txt has no %s", name)
}

func unpack(archive []byte, bin string) ([]byte, error) {
	if zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive))); err == nil {
		for _, f := range zr.File {
			if f.Name == bin {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, fmt.Errorf("the archive has no %s", bin)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("read the archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("the archive has no %s: %w", bin, err)
		}
		if h.Name == bin {
			return io.ReadAll(tr)
		}
	}
}

// replace puts bin at exe without ever leaving a half-written file there:
// it writes beside exe and renames into place.
func replace(exe string, bin []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".prowl-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Rename(tmp.Name(), exe)
	}
	old := OldBinary(exe)
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		_ = os.Rename(old, exe)
		return err
	}
	return nil
}
