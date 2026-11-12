package core_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jedisct1/go-minisign"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
)

// The key prowl verifies releases with is the one in minisign.pub, which
// people check downloads with by hand.
func TestReleaseKey(t *testing.T) {
	b, err := os.ReadFile("../../minisign.pub")
	if err != nil {
		t.Fatal(err)
	}
	if ls := strings.Split(strings.TrimSpace(string(b)), "\n"); len(ls) != 2 || ls[1] != core.ReleaseKey {
		t.Fatalf("core.ReleaseKey %q doesn't match minisign.pub", core.ReleaseKey)
	}
	if _, err := minisign.NewPublicKey(core.ReleaseKey); err != nil {
		t.Fatal(err)
	}
}

// running pretends the test binary is the release build v.
func running(t *testing.T, v string) {
	t.Helper()
	old, key := core.Version, core.ReleaseKey
	core.Version = v
	t.Cleanup(func() { core.Version, core.ReleaseKey = old, key })
}

// signer is a minisign key; trust makes it prowl's release key.
func signer(t *testing.T, id byte, trust bool) minisign.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sk := minisign.PrivateKey{SignatureAlgorithm: [2]byte{'E', 'd'}, KeyId: [8]byte{id}}
	copy(sk.SecretKey[:], priv)
	if trust {
		pk := sk.PublicKey()
		core.ReleaseKey = base64.StdEncoding.EncodeToString(append(append(pk.SignatureAlgorithm[:], pk.KeyId[:]...), pk.PublicKey[:]...))
	}
	return sk
}

// publish serves release tag on the fake GitHub: an archive holding bin as
// prowl, checksums.txt and its signature by sk. tamper changes the archive
// after it was summed.
func publish(t *testing.T, fake *ghtest.GitHub, tag, bin string, sk minisign.PrivateKey, tamper bool) {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "LICENSE", Mode: 0o644, Size: 3})
	_, _ = tw.Write([]byte("MIT"))
	_ = tw.WriteHeader(&tar.Header{Name: "prowl", Mode: 0o755, Size: int64(len(bin))})
	_, _ = tw.Write([]byte(bin))
	_ = tw.Close()
	_ = gz.Close()
	name := fmt.Sprintf("prowl_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(archive.Bytes())
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	sig, err := sk.Sign([]byte(sums), minisign.SignOptions{Hashed: true, TrustedComment: "prowl " + tag})
	if err != nil {
		t.Fatal(err)
	}
	if tamper {
		archive.WriteString("x")
	}
	fake.Release = &gh.Release{Tag: tag, Notes: "- reviews run faster", URL: "https://github.com/tmstorm/prowl/releases/tag/" + tag,
		Assets: []gh.Asset{{ID: 1, Name: name}, {ID: 2, Name: "checksums.txt"}, {ID: 3, Name: "checksums.txt.minisig"}}}
	fake.Assets = map[int64][]byte{1: archive.Bytes(), 2: []byte(sums), 3: sig.Encode()}
}

// installed is a prowl binary in a folder prowl owns, as the install script
// leaves it.
func installed(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "prowl")
	if err := os.WriteFile(exe, []byte("v0.1.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// prowl asks GitHub for its newest release once a day, never as a build
// from source or with check_updates off, and says how it gets updated: by
// itself where the install script put it, by the package manager in /usr.
func TestUpdateCheck(t *testing.T) {
	running(t, "0.1.0")
	fake := &ghtest.GitHub{}
	publish(t, fake, "v0.2.0", "v0.2.0", signer(t, 1, true), false)
	co, _ := newCore(t, fake)
	exe := installed(t)
	if _, err := co.Update(func(c *config.Config) error { c.CheckUpdates = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := co.CheckRelease(false); err != nil || fake.ReleaseCalls() != 0 {
		t.Fatalf("check_updates off asked GitHub: %v, %d calls", err, fake.ReleaseCalls())
	}
	_, _ = co.Update(func(c *config.Config) error { c.CheckUpdates = true; return nil })
	if err := co.CheckRelease(false); err != nil {
		t.Fatal(err)
	}
	r := co.Release(exe)
	if !r.Newer() || r.Latest != "v0.2.0" || r.Running != "v0.1.0" || r.Command != "" || r.Installed {
		t.Fatalf("release: %+v", r)
	}
	_ = co.CheckRelease(false)
	if fake.ReleaseCalls() != 1 {
		t.Fatalf("asked GitHub %d times in a day", fake.ReleaseCalls())
	}
	_ = co.CheckRelease(true)
	if fake.ReleaseCalls() != 2 {
		t.Fatal("a forced check asks again")
	}
	if runtime.GOOS == "linux" {
		if cmd := co.Release("/usr/bin/prowl").Command; cmd != "yay -S prowl-bin" {
			t.Errorf("a package's prowl: %q", cmd)
		}
	}
	core.Version = ""
	_ = co.CheckRelease(true)
	if fake.ReleaseCalls() != 2 || co.Release(exe).Newer() {
		t.Error("a build from source never checks")
	}
}

// prowl replaces its own binary with a release only when the archive matches
// checksums.txt and checksums.txt is signed with prowl's release key.
func TestUpgrade(t *testing.T) {
	running(t, "0.1.0")
	fake := &ghtest.GitHub{}
	co, _ := newCore(t, fake)
	exe := installed(t)
	key := signer(t, 1, true)
	for name, bad := range map[string]func(){
		"tampered archive": func() { publish(t, fake, "v0.2.0", "v0.2.0", key, true) },
		"unknown key":      func() { publish(t, fake, "v0.2.0", "v0.2.0", signer(t, 2, false), false) },
		"older release": func() {
			publish(t, fake, "v0.2.0", "v0.2.0", key, false)
			fake.Release.Tag = "v0.3.0"
		},
	} {
		bad()
		if v, err := co.Upgrade(exe); err == nil || v != "" {
			t.Fatalf("%s: installed %q", name, v)
		}
		if b, _ := os.ReadFile(exe); string(b) != "v0.1.0" {
			t.Fatalf("%s replaced the binary", name)
		}
	}
	publish(t, fake, "v0.2.0", "v0.2.0", key, false)
	v, err := co.Upgrade(exe)
	if err != nil || v != "v0.2.0" {
		t.Fatalf("upgrade: %q, %v", v, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "v0.2.0" {
		t.Fatalf("binary is %q", b)
	}
	if fi, _ := os.Stat(exe); runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 {
		t.Error("the new binary isn't executable")
	}
	if r := co.Release(exe); !r.Installed {
		t.Errorf("a restart runs the update: %+v", r)
	}
	if v, err := co.Upgrade(exe); err != nil || v != "" {
		t.Errorf("an installed release isn't installed again: %q, %v", v, err)
	}
	if runtime.GOOS == "linux" {
		if _, err := co.Upgrade("/usr/bin/prowl"); err == nil || !strings.Contains(err.Error(), "yay -S prowl-bin") {
			t.Errorf("a package's prowl is left to the package manager: %v", err)
		}
	}
}

// In the background a release prowl can't verify isn't downloaded again on
// every check, but one that failed to download is tried again.
func TestAutoUpgrade(t *testing.T) {
	running(t, "0.1.0")
	fake := &ghtest.GitHub{}
	publish(t, fake, "v0.2.0", "v0.2.0", signer(t, 2, false), false)
	signer(t, 1, true)
	co, _ := newCore(t, fake)
	exe := installed(t)
	if _, err := co.AutoUpgrade(exe); err == nil {
		t.Fatal("a release signed by another key installed")
	}
	if v, err := co.AutoUpgrade(exe); err != nil || v != "" {
		t.Fatalf("the failed release was tried again: %q, %v", v, err)
	}
	_, _ = co.Update(func(c *config.Config) error { c.AutoUpdate = false; return nil })
	key := signer(t, 3, true)
	publish(t, fake, "v0.3.0", "v0.3.0", key, false)
	_ = co.CheckRelease(true)
	if v, _ := co.AutoUpgrade(exe); v != "" {
		t.Fatal("auto_update off installed a release")
	}
	_, _ = co.Update(func(c *config.Config) error { c.AutoUpdate = true; return nil })
	assets := fake.Release.Assets
	fake.Release.Assets = assets[:1]
	if _, err := co.AutoUpgrade(exe); err == nil {
		t.Fatal("a release without checksums installed")
	}
	fake.Release.Assets = assets
	if v, err := co.AutoUpgrade(exe); err != nil || v != "v0.3.0" {
		t.Fatalf("auto upgrade: %q, %v", v, err)
	}
}

// The first start of a release shows its notes once.
func TestWhatsNew(t *testing.T) {
	running(t, "0.2.0")
	fake := &ghtest.GitHub{}
	publish(t, fake, "v0.2.0", "v0.2.0", signer(t, 1, true), false)
	co, _ := newCore(t, fake)
	if v, _ := co.WhatsNew(); v != "" {
		t.Fatal("no notes before the first check")
	}
	_ = co.CheckRelease(false)
	if v, notes := co.WhatsNew(); v != "v0.2.0" || notes != "- reviews run faster" {
		t.Fatalf("what's new: %q %q", v, notes)
	}
	if v, _ := co.WhatsNew(); v != "" {
		t.Fatal("what's new shows once")
	}
}
