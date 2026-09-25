package config

import (
	"context"
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func newDownloadServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func assertNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to not exist, stat err=%v", path, err)
	}
}

func TestDownloadSuccess(t *testing.T) {
	srv := newDownloadServer(t, "hello world", http.StatusOK)
	dest := filepath.Join(t.TempDir(), "sub", "file.bin")

	if err := Download(context.Background(), srv.Client(), srv.URL+"/file.bin", dest, 0, ""); err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("unexpected content %q", got)
	}
	assertNoFile(t, dest+".tmp")
}

func TestDownloadWithChecksum(t *testing.T) {
	body := "verified payload"
	sum := sha256.Sum256([]byte(body))
	srv := newDownloadServer(t, body, http.StatusOK)
	dest := filepath.Join(t.TempDir(), "file.bin")

	if err := Download(context.Background(), srv.Client(), srv.URL, dest, crypto.SHA256, strings.ToUpper(hex.EncodeToString(sum[:]))); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !FileHashMatches(dest, crypto.SHA256, hex.EncodeToString(sum[:])) {
		t.Fatalf("FileHashMatches should be true for freshly downloaded file")
	}
}

func TestDownloadHTTPError(t *testing.T) {
	srv := newDownloadServer(t, "nope", http.StatusNotFound)
	dest := filepath.Join(t.TempDir(), "file.bin")

	err := Download(context.Background(), srv.Client(), srv.URL, dest, 0, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected HTTP 404 error, got %v", err)
	}
	assertNoFile(t, dest)
	assertNoFile(t, dest+".tmp")
}

func TestDownloadChecksumMismatch(t *testing.T) {
	srv := newDownloadServer(t, "tampered", http.StatusOK)
	dest := filepath.Join(t.TempDir(), "file.bin")

	err := Download(context.Background(), srv.Client(), srv.URL, dest, crypto.SHA256, strings.Repeat("00", 32))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	assertNoFile(t, dest)
	assertNoFile(t, dest+".tmp")
}

func TestDownloadRejectsUnsupportedHash(t *testing.T) {
	srv := newDownloadServer(t, "x", http.StatusOK)
	err := Download(context.Background(), srv.Client(), srv.URL, filepath.Join(t.TempDir(), "f"), crypto.MD5, "abcd")
	if err == nil {
		t.Fatal("expected error for unsupported hash")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	if err := WriteFileAtomic(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	if err := WriteFileAtomic(path, []byte(`{"a":2}`), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic overwrite: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != `{"a":2}` {
		t.Fatalf("unexpected content %q err=%v", got, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one file, got %d", len(entries))
	}
}

func TestReplaceSymlinkOverRegularFile(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "kernel")
	if err := os.WriteFile(link, []byte("old regular file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "flatcar", "1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "flatcar", "1.0.0", "kernel"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ReplaceSymlink(filepath.Join("flatcar", "1.0.0", "kernel"), link); err != nil {
		t.Fatalf("ReplaceSymlink: %v", err)
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != filepath.Join("flatcar", "1.0.0", "kernel") {
		t.Fatalf("unexpected target %q", target)
	}
	got, err := os.ReadFile(link)
	if err != nil || string(got) != "new" {
		t.Fatalf("unexpected content %q err=%v", got, err)
	}
	assertNoFile(t, link+".tmp")
}

func TestCleanRelPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"config/ignition.yaml", "config/ignition.yaml", true},
		{"./config//ignition.yaml", "config/ignition.yaml", true},
		{"a/b/../c.yaml", "a/c.yaml", true},
		{"", "", false},
		{".", "", false},
		{"..", "", false},
		{"../etc/passwd", "", false},
		{"a/../../x", "", false},
		{"/etc/passwd", "", false},
		{"config/\x00.yaml", "", false},
	}
	for _, tc := range tests {
		got, err := CleanRelPath(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("CleanRelPath(%q): ok=%v err=%v", tc.in, tc.ok, err)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("CleanRelPath(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestServerAddressHelpers(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	LoadConfig()

	viper.Set(ServerIP, "192.168.1.10")
	viper.Set(HttpPort, 8080)
	viper.Set(ServerHttpPort, 0)
	if got := EffectiveServerHttpPort(); got != 8080 {
		t.Fatalf("EffectiveServerHttpPort=%d want httpPort 8080", got)
	}
	if got := ServerHostPort(); got != "192.168.1.10:8080" {
		t.Fatalf("ServerHostPort=%q", got)
	}
	if got := ClientRegistry(); got != "192.168.1.10:8080" {
		t.Fatalf("ClientRegistry=%q", got)
	}

	viper.Set(ServerHttpPort, 80)
	if got := ServerHostPort(); got != "192.168.1.10" {
		t.Fatalf("ServerHostPort must omit :80, got %q", got)
	}
	if got := ClientRegistry(); got != "192.168.1.10:80" {
		t.Fatalf("ClientRegistry keeps the explicit port for OCI refs, got %q", got)
	}

	viper.Set(ServerHttpPort, 9090)
	if got := ServerHostPort(); got != "192.168.1.10:9090" {
		t.Fatalf("explicit serverHttpPort must win, got %q", got)
	}
}

func TestResolveServerAddress(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	LoadConfig()
	viper.Set(HttpPort, 18080)
	viper.Set(ServerHttpPort, 0)
	viper.Set(ServerIP, "10.9.8.7")

	if err := ResolveServerAddress(); err != nil {
		t.Fatalf("ResolveServerAddress: %v", err)
	}
	if viper.GetString(ServerIP) != "10.9.8.7" {
		t.Fatalf("explicit serverIP must be kept, got %q", viper.GetString(ServerIP))
	}
	if viper.GetInt(ServerHttpPort) != 18080 {
		t.Fatalf("serverHttpPort should default to httpPort, got %d", viper.GetInt(ServerHttpPort))
	}

	viper.Set(ServerIP, "")
	if err := ResolveServerAddress(); err != nil {
		t.Skipf("no default route in this environment: %v", err)
	}
	ip := net.ParseIP(viper.GetString(ServerIP))
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
		t.Fatalf("autodetected serverIP %q should be a real interface address", viper.GetString(ServerIP))
	}
}

func TestValidateDoInstallClearOn(t *testing.T) {
	for _, ok := range []string{ClearOnIgnition, ClearOnBooted, ClearOnNextBoot} {
		if err := ValidateDoInstallClearOn(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "never", "Ignition", "nextboot"} {
		if err := ValidateDoInstallClearOn(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestBluefinDefaults(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	LoadConfig()
	if got := viper.GetString(BluefinRepo); got != "projectbluefin/server" {
		t.Fatalf("bluefinRepo default %q", got)
	}
	if got := viper.GetDuration(InstallMinDuration); got != 3*time.Minute {
		t.Fatalf("installMinDuration default %s", got)
	}
	t.Setenv("BOOTY_GITHUBTOKEN", "ghp_x")
	t.Setenv("BOOTY_BLUEFINVERSION", "26.08.0")
	if viper.GetString(GithubToken) != "ghp_x" || viper.GetString(BluefinVersion) != "26.08.0" {
		t.Fatalf("env binding: token=%q version=%q", viper.GetString(GithubToken), viper.GetString(BluefinVersion))
	}
	viper.Set(DataDir, "/d")
	if got := BluefinCurrentManifestPath(); got != "/d/bluefin/current/manifest.json" {
		t.Fatalf("BluefinCurrentManifestPath=%q", got)
	}
}
