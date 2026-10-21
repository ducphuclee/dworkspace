package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// salt.md keeps the cloudflared it manages current.
//
// It downloads cloudflared once, into the data directory, the first time a
// tunnel starts, and runs it with --no-autoupdate: cloudflared replacing itself
// mid-connection is not something a supervised child should do, and a quick
// tunnel would come back under a new address. Until 1.1.2 that meant the
// binary stayed at the version of its first download for as long as the
// instance lived. Cloudflare's dashboard marks such a connector as outdated,
// fixes never arrive, and Cloudflare supports a release for about a year.
//
// So salt.md looks once a day (and when an admin asks) for a newer release,
// downloads the asset for this platform from the release itself, checks it
// against the SHA256 GitHub records for that asset, swaps it in, and restarts a
// named tunnel, which is gone for a few seconds.
//
// GitHub's digest and not the list in the release notes: the notes are written
// before the macOS archives are finished, and on 2026.10.1 the sum they give
// for cloudflared-darwin-arm64.tgz is not the file's. For the Linux assets the
// two agree. The notes are the fallback for a release GitHub has no digest for. A quick tunnel keeps running on the
// old binary until its next start, because a restart would change its address.
//
// Only the copy salt.md downloaded itself is ever replaced. A cloudflared from
// the PATH belongs to the system's package manager; for that one the settings
// only say that it is behind.

var (
	cloudflaredReleaseAPI   = "https://api.github.com/repos/cloudflare/cloudflared/releases/latest"
	cloudflaredDownloadBase = "https://github.com/cloudflare/cloudflared/releases/download/"
	// The checksums sit in the release notes, one "asset: sha256" per line.
	cloudflaredSumRe     = regexp.MustCompile(`(?m)^\s*(cloudflared-[A-Za-z0-9._-]+):\s*([0-9a-f]{64})\s*$`)
	cloudflaredVersionRe = regexp.MustCompile(`cloudflared version (\d+(?:\.\d+)+)`)
)

type cloudflaredRelease struct {
	Version string
	Sums    map[string]string
}

// cloudflaredUpdates is what the settings show about the binary in use.
type cloudflaredUpdates struct {
	mu        sync.Mutex
	installed string
	// The binary the installed version was read from, by modification time:
	// somebody replacing it by hand must not leave the settings on the old one.
	readFrom time.Time
	latest   string
	checked  time.Time
	lastErr  string
	busy     bool
}

func fetchCloudflaredRelease(ctx context.Context) (cloudflaredRelease, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", cloudflaredReleaseAPI, nil)
	if err != nil {
		return cloudflaredRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "salt.md/"+Version)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return cloudflaredRelease{}, fmt.Errorf("release list unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return cloudflaredRelease{}, fmt.Errorf("release list answered HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
		Assets  []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return cloudflaredRelease{}, fmt.Errorf("release list unreadable: %w", err)
	}
	rel := cloudflaredRelease{Version: strings.TrimPrefix(strings.TrimSpace(body.TagName), "v"), Sums: map[string]string{}}
	if !cloudflaredVersionRe.MatchString("cloudflared version " + rel.Version) {
		return cloudflaredRelease{}, fmt.Errorf("release list names no version")
	}
	for _, m := range cloudflaredSumRe.FindAllStringSubmatch(body.Body, -1) {
		rel.Sums[m[1]] = m[2]
	}
	for _, a := range body.Assets {
		if d := strings.TrimPrefix(a.Digest, "sha256:"); len(d) == 64 && d != a.Digest {
			rel.Sums[a.Name] = strings.ToLower(d)
		}
	}
	return rel, nil
}

// cloudflaredVersion asks a binary for its version.
func cloudflaredVersion(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cloudflared --version: %w", err)
	}
	m := cloudflaredVersionRe.FindSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("cloudflared --version said nothing readable")
	}
	return string(m[1]), nil
}

// newerVersion reports whether a is a later cloudflared release than b. Both
// are year.month.patch; anything that does not read as numbers is never newer,
// so a surprise in the release list cannot trigger a swap.
func newerVersion(a, b string) bool {
	na, okA := versionParts(a)
	nb, okB := versionParts(b)
	if !okA || !okB {
		return false
	}
	for i := 0; i < len(na) || i < len(nb); i++ {
		var x, y int
		if i < len(na) {
			x = na[i]
		}
		if i < len(nb) {
			y = nb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func versionParts(v string) ([]int, bool) {
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// installCloudflared downloads one release's asset for this platform, checks
// it against the published checksum before anything else happens to it, and
// puts the binary at dest in one rename. A process still running the old file
// keeps its copy; the next start gets the new one.
func installCloudflared(rel cloudflaredRelease, dest string) error {
	asset, isTgz, err := cloudflaredAsset()
	if err != nil {
		return err
	}
	want := rel.Sums[asset]
	if want == "" {
		return fmt.Errorf("release %s publishes no checksum for %s; not installing an unverified binary", rel.Version, asset)
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(cloudflaredDownloadBase + rel.Version + "/" + asset)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	raw, err := os.CreateTemp(filepath.Dir(dest), ".cloudflared-download-")
	if err != nil {
		return err
	}
	defer os.Remove(raw.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(raw, h), io.LimitReader(resp.Body, 200<<20)); err != nil {
		raw.Close()
		return fmt.Errorf("download failed: %w", err)
	}
	raw.Close()
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch for %s %s: got %s, the release records %s", asset, rel.Version, got, want)
	}

	tmp := dest + ".tmp"
	defer os.Remove(tmp)
	if isTgz {
		if err := extractCloudflared(raw.Name(), tmp); err != nil {
			return err
		}
	} else if err := os.Rename(raw.Name(), tmp); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func extractCloudflared(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("cloudflared not found in the archive")
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != "cloudflared" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, io.LimitReader(tr, 200<<20)); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
}

// managedCloudflared is the path of the copy salt.md downloads itself.
func (s *Server) managedCloudflared() string {
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(s.dataDir, "bin", name)
}

// checkCloudflared reads the version in use and the newest release, and with
// install set replaces the managed copy when it is behind. It reports whether
// it replaced anything.
func (s *Server) checkCloudflared(install bool) (bool, error) {
	u := &s.cloudflared
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return false, fmt.Errorf("a check is already running")
	}
	u.busy = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
	}()

	done := func(installed, latest string, err error) error {
		u.mu.Lock()
		if installed != "" {
			u.installed = installed
		}
		if latest != "" {
			u.latest = latest
		}
		u.checked = time.Now()
		u.lastErr = ""
		if err != nil {
			u.lastErr = err.Error()
		}
		u.mu.Unlock()
		return err
	}

	bin, found := s.cloudflaredPath()
	if !found {
		return false, nil
	}
	installed, err := cloudflaredVersion(bin)
	if err != nil {
		return false, done("", "", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	rel, err := fetchCloudflaredRelease(ctx)
	if err != nil {
		return false, done(installed, "", err)
	}
	if !install || bin != s.managedCloudflared() || !newerVersion(rel.Version, installed) {
		return false, done(installed, rel.Version, nil)
	}
	if err := installCloudflared(rel, bin); err != nil {
		log.Printf("tunnel: cloudflared update to %s failed: %v", rel.Version, err)
		return false, done(installed, rel.Version, err)
	}
	now, err := cloudflaredVersion(bin)
	if err != nil {
		return true, done("", rel.Version, err)
	}
	log.Printf("tunnel: cloudflared %s replaced by %s (checksum verified)", installed, now)
	if err := s.restartNamedTunnel(); err != nil {
		log.Printf("tunnel: restart after the update failed: %v", err)
		return true, done(now, rel.Version, err)
	}
	return true, done(now, rel.Version, nil)
}

// restartNamedTunnel brings a running named tunnel back up on the binary now
// on disk. The stored token and autostart stay as they are; only the process
// is replaced. A quick tunnel is left alone: restarting it changes its address.
func (s *Server) restartNamedTunnel() error {
	t := &s.tunnel
	t.mu.Lock()
	running := t.cmd != nil && t.mode == "token"
	t.mu.Unlock()
	if !running {
		return nil
	}
	s.SignalTunnelStop()
	s.AwaitTunnelStop(10 * time.Second)
	return s.StartTunnel("token", "")
}

// cloudflaredUpdateLoop checks a few minutes after start and then daily. It
// does nothing at all on an instance that never ran a tunnel: no binary, no
// request to anybody.
func (s *Server) cloudflaredUpdateLoop() {
	timer := time.NewTimer(3 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-s.stopCleanup:
			return
		case <-timer.C:
		}
		if _, found := s.cloudflaredPath(); found {
			if _, err := s.checkCloudflared(s.setting("tunnel_autoupdate", "1") != "0"); err != nil {
				log.Printf("tunnel: cloudflared check: %v", err)
			}
		}
		timer.Reset(24 * time.Hour)
	}
}

// cloudflaredStatus is the part of the public-access answer about the binary.
func (s *Server) cloudflaredStatus() map[string]any {
	u := &s.cloudflared
	u.mu.Lock()
	installed, latest, checked, lastErr, busy := u.installed, u.latest, u.checked, u.lastErr, u.busy
	u.mu.Unlock()
	bin, found := s.cloudflaredPath()
	if found {
		// The version alone costs no network, only a look at the file: read it
		// again whenever the file changed since.
		if st, err := os.Stat(bin); err == nil {
			u.mu.Lock()
			stale := installed == "" || !st.ModTime().Equal(u.readFrom)
			u.mu.Unlock()
			if stale {
				if v, err := cloudflaredVersion(bin); err == nil {
					installed = v
					u.mu.Lock()
					u.installed, u.readFrom = v, st.ModTime()
					u.mu.Unlock()
				}
			}
		}
	}
	checkedAt := ""
	if !checked.IsZero() {
		checkedAt = checked.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"cloudflaredVersion":    installed,
		"cloudflaredLatest":     latest,
		"cloudflaredBehind":     installed != "" && latest != "" && newerVersion(latest, installed),
		"cloudflaredManaged":    found && bin == s.managedCloudflared(),
		"cloudflaredCheckedAt":  checkedAt,
		"cloudflaredCheckError": lastErr,
		"cloudflaredUpdating":   busy,
		"cloudflaredAutoUpdate": s.setting("tunnel_autoupdate", "1") != "0",
	}
}
