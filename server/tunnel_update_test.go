package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCloudflaredVersionsCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		newer bool
	}{
		{"2026.10.1", "2026.7.2", true},
		{"2026.10.0", "2026.9.3", true},
		{"2026.7.2", "2026.7.2", false},
		{"2026.7.2", "2026.10.1", false},
		{"2027.1.0", "2026.12.9", true},
		{"2026.10", "2026.9.9", true},
		{"latest", "2026.7.2", false},
		{"2026.10.1-rc1", "2026.7.2", false},
	} {
		if got := newerVersion(c.a, c.b); got != c.newer {
			t.Errorf("newerVersion(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// fakeCloudflared is a stand-in binary that answers --version like the real
// one, in the shape this platform's release asset has.
func fakeCloudflared(t *testing.T, version string) (asset string, payload []byte, binary []byte) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in binary is a shell script")
	}
	binary = []byte("#!/bin/sh\necho \"cloudflared version " + version + " (built 2026-10-15-17:00 UTC)\"\n")
	asset, isTgz, err := cloudflaredAsset()
	if err != nil {
		t.Skip(err.Error())
	}
	if !isTgz {
		return asset, binary, binary
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "cloudflared", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
	tw.Write(binary)
	tw.Close()
	gz.Close()
	return asset, buf.Bytes(), binary
}

// releaseServer plays GitHub: the release list and the download.
func releaseServer(t *testing.T, version, asset string, payload []byte, publishedSum string, digest ...string) {
	t.Helper()
	notes := "### SHA256 Checksums:\n```\ncloudflared-unrelated: " + strings.Repeat("0", 64) + "\n" + asset + ": " + publishedSum + "\n```\n"
	assets := []map[string]string{}
	if len(digest) > 0 {
		assets = append(assets, map[string]string{"name": asset, "digest": "sha256:" + digest[0]})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/latest":
			json.NewEncoder(w).Encode(map[string]any{"tag_name": version, "body": notes, "assets": assets})
		case r.URL.Path == "/download/"+version+"/"+asset:
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldAPI, oldBase := cloudflaredReleaseAPI, cloudflaredDownloadBase
	cloudflaredReleaseAPI, cloudflaredDownloadBase = srv.URL+"/latest", srv.URL+"/download/"
	t.Cleanup(func() { cloudflaredReleaseAPI, cloudflaredDownloadBase = oldAPI, oldBase })
}

func sumOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestAnOutdatedCloudflaredIsReplacedAfterTheChecksum(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // only the managed copy counts here
	s := testServer(t)
	asset, payload, _ := fakeCloudflared(t, "2026.10.1")
	_, _, old := fakeCloudflared(t, "2026.7.2")
	bin := s.managedCloudflared()
	os.MkdirAll(filepath.Dir(bin), 0o755)
	if err := os.WriteFile(bin, old, 0o755); err != nil {
		t.Fatal(err)
	}
	releaseServer(t, "2026.10.1", asset, payload, sumOf(payload))

	// Looking only: behind, nothing touched.
	if swapped, err := s.checkCloudflared(false); swapped || err != nil {
		t.Fatalf("a look replaced the binary (%v) or failed: %v", swapped, err)
	}
	st := s.cloudflaredStatus()
	if st["cloudflaredVersion"] != "2026.7.2" || st["cloudflaredLatest"] != "2026.10.1" || st["cloudflaredBehind"] != true || st["cloudflaredManaged"] != true {
		t.Fatalf("status after looking: %v", st)
	}

	if swapped, err := s.checkCloudflared(true); !swapped || err != nil {
		t.Fatalf("update: swapped %v, err %v", swapped, err)
	}
	if v, err := cloudflaredVersion(bin); err != nil || v != "2026.10.1" {
		t.Fatalf("installed afterwards: %q %v", v, err)
	}
	if st := s.cloudflaredStatus(); st["cloudflaredBehind"] != false || st["cloudflaredVersion"] != "2026.10.1" {
		t.Errorf("status after the update: %v", st)
	}
	// Current now: a second run changes nothing.
	if swapped, err := s.checkCloudflared(true); swapped || err != nil {
		t.Errorf("a current binary was replaced again (%v) or failed: %v", swapped, err)
	}
}

func TestACloudflaredThatFailsItsChecksumIsNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	s := testServer(t)
	asset, payload, _ := fakeCloudflared(t, "2026.10.1")
	_, _, old := fakeCloudflared(t, "2026.7.2")
	bin := s.managedCloudflared()
	os.MkdirAll(filepath.Dir(bin), 0o755)
	os.WriteFile(bin, old, 0o755)
	releaseServer(t, "2026.10.1", asset, payload, strings.Repeat("a", 64))

	swapped, err := s.checkCloudflared(true)
	if swapped || err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("a binary with the wrong checksum: swapped %v, err %v", swapped, err)
	}
	if v, _ := cloudflaredVersion(bin); v != "2026.7.2" {
		t.Errorf("the old binary is gone: %q", v)
	}
	if st := s.cloudflaredStatus(); !strings.Contains(st["cloudflaredCheckError"].(string), "checksum mismatch") {
		t.Errorf("the settings do not say why: %v", st)
	}
	entries, _ := os.ReadDir(filepath.Dir(bin))
	if len(entries) != 1 {
		t.Errorf("leftovers beside the binary: %v", entries)
	}
}

// A release whose notes carry no checksum for this platform is not installed,
// not even as the first download.
func TestAnUnverifiableCloudflaredIsNeverDownloaded(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	s := testServer(t)
	_, payload, _ := fakeCloudflared(t, "2026.10.1")
	releaseServer(t, "2026.10.1", "cloudflared-some-other-platform", payload, sumOf(payload))
	if err := s.downloadCloudflared(s.managedCloudflared()); err == nil || !strings.Contains(err.Error(), "no checksum") {
		t.Fatalf("download without a checksum: %v", err)
	}
	if _, err := os.Stat(s.managedCloudflared()); err == nil {
		t.Error("an unverified binary was installed")
	}
}

// On 2026.10.1 the release notes give a sum for the macOS archive that is not
// the file's, while GitHub's own digest is. The digest decides.
func TestGitHubsDigestDecidesOverTheReleaseNotes(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	s := testServer(t)
	asset, payload, _ := fakeCloudflared(t, "2026.10.1")
	releaseServer(t, "2026.10.1", asset, payload, strings.Repeat("b", 64), sumOf(payload))
	if err := s.downloadCloudflared(s.managedCloudflared()); err != nil {
		t.Fatalf("a download matching GitHub's digest was refused: %v", err)
	}
	if v, err := cloudflaredVersion(s.managedCloudflared()); err != nil || v != "2026.10.1" {
		t.Errorf("installed: %q %v", v, err)
	}
}
