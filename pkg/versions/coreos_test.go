package versions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

const coreOSTestVersion = "41.20260101.3.0"

// fakeCoreOS serves a streams JSON for x86_64 whose artifact locations are
// http:// (so the https-only check rejects them and the constructed
// --coreOSURL is used) plus the artifacts. sums overrides the published
// sha256 per artifact type; "-" omits it.
func fakeCoreOS(t *testing.T, sums map[string]string) (*httptest.Server, map[string]*atomic.Int32) {
	t.Helper()
	names := coreOSArtifactNames(coreOSTestVersion, "x86_64")
	routes := map[string][]byte{}
	hits := map[string]*atomic.Int32{}
	var pxe []string
	for _, typ := range []string{"kernel", "initramfs", "rootfs"} {
		data := []byte("coreos-" + typ)
		path := fmt.Sprintf("/stable/%s/x86_64/%s", coreOSTestVersion, names[typ])
		routes[path] = data
		hits[path] = &atomic.Int32{}
		sum := sha256.Sum256(data)
		entry := fmt.Sprintf(`"location": "http://insecure.example/%s"`, names[typ])
		switch s, ok := sums[typ]; {
		case !ok:
			entry += fmt.Sprintf(`, "sha256": %q`, hex.EncodeToString(sum[:]))
		case s != "-":
			entry += fmt.Sprintf(`, "sha256": %q`, s)
		}
		pxe = append(pxe, fmt.Sprintf(`%q: {%s}`, typ, entry))
	}
	hits["/streams/stable.json"] = &atomic.Int32{}
	routes["/streams/stable.json"] = []byte(fmt.Sprintf(`{"stream":"stable","architectures":{"x86_64":{"artifacts":{"metal":{"release":%q,"formats":{"pxe":{%s}}}}}}}`, coreOSTestVersion, strings.Join(pxe, ",")))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if h := hits[r.URL.Path]; h != nil {
			h.Add(1)
		}
		if _, err := w.Write(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func setupCoreOS(t *testing.T, srvURL string) string {
	t.Helper()
	dir := setupVerify(t)
	viper.Set(config.CoreOSURL, srvURL+"/%s/%s/%s")
	viper.Set(config.CoreOSStreamsURL, srvURL+"/streams/%s.json")
	metadataBackoff = time.Millisecond
	t.Cleanup(func() {
		metadataBackoff = time.Second
		state.ClearSyncError(OSCoreOS)
	})
	return dir
}

func TestLoadRemoteCoreOSVersionParsesStreams(t *testing.T) {
	srv, _ := fakeCoreOS(t, nil)
	setupCoreOS(t, srv.URL)
	body, v, err := LoadRemoteCoreOSVersion(context.Background(), "x86_64")
	if err != nil || v != coreOSTestVersion || len(body) == 0 {
		t.Fatalf("v=%q err=%v", v, err)
	}
	if state.RemoteCoreOSVersion() != coreOSTestVersion {
		t.Errorf("remote state = %q", state.RemoteCoreOSVersion())
	}
	if _, _, err := LoadRemoteCoreOSVersion(context.Background(), "s390x"); err == nil {
		t.Error("missing architecture accepted")
	}
}

func TestExtractCoreOSLocationHTTPSOnly(t *testing.T) {
	body := []byte(`{"architectures":{"x86_64":{"artifacts":{"metal":{"formats":{"pxe":{
		"kernel":{"location":"https://ok.example/k"},
		"initramfs":{"location":"http://bad.example/i"},
		"rootfs":{"location":"file:///etc/passwd"}}}}}}}}`)
	if loc, err := extractCoreOSLocation(body, "x86_64", "kernel"); err != nil || loc != "https://ok.example/k" {
		t.Errorf("kernel = %q, %v", loc, err)
	}
	for _, typ := range []string{"initramfs", "rootfs", "missing"} {
		if loc, err := extractCoreOSLocation(body, "x86_64", typ); err == nil {
			t.Errorf("%s accepted: %q", typ, loc)
		}
	}
	if _, err := extractCoreOSLocation(nil, "x86_64", "kernel"); err == nil {
		t.Error("empty body accepted")
	}
}

func TestCoreOSVersionCheckInstalls(t *testing.T) {
	srv, _ := fakeCoreOS(t, nil)
	dir := setupCoreOS(t, srv.URL)

	CoreOSVersionCheck()

	if got := state.CurrentCoreOSVersion(); got != coreOSTestVersion {
		t.Fatalf("current = %q", got)
	}
	for typ, name := range coreOSArtifactNames(coreOSTestVersion, "x86_64") {
		if got, err := os.ReadFile(filepath.Join(dir, OSCoreOS, coreOSTestVersion, name)); err != nil || string(got) != "coreos-"+typ {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	if _, ok := state.SyncErrors()[OSCoreOS]; ok {
		t.Error("sync error after success")
	}
}

func TestCoreOSVersionCheckFailsClosedWithoutChecksum(t *testing.T) {
	for name, sums := range map[string]map[string]string{
		"missing":  {"rootfs": "-"},
		"empty":    {"rootfs": ""},
		"mismatch": {"kernel": strings.Repeat("0", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			srv, hits := fakeCoreOS(t, sums)
			dir := setupCoreOS(t, srv.URL)

			CoreOSVersionCheck()

			if got := state.CurrentCoreOSVersion(); got == coreOSTestVersion {
				t.Fatal("advanced to a release with an unverified artifact")
			}
			if _, err := os.Stat(filepath.Join(dir, OSCoreOS, coreOSTestVersion)); !os.IsNotExist(err) {
				t.Error("partial release left on disk")
			}
			if CurrentRelease(OSCoreOS) != "" {
				t.Error("current linked")
			}
			if name == "missing" || name == "empty" {
				rootfs := fmt.Sprintf("/stable/%s/x86_64/%s", coreOSTestVersion, coreOSArtifactNames(coreOSTestVersion, "x86_64")["rootfs"])
				if hits[rootfs].Load() != 0 {
					t.Error("artifact without a checksum downloaded")
				}
			}
			if _, ok := state.SyncErrors()[OSCoreOS]; !ok {
				t.Error("no sync error recorded")
			}
		})
	}
}

func TestCoreOSStreamsRetriedThenSyncError(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	setupCoreOS(t, srv.URL)

	CoreOSVersionCheck()

	if int(n.Load()) != metadataAttempts {
		t.Errorf("streams fetched %d times, want %d", n.Load(), metadataAttempts)
	}
	if _, ok := state.SyncErrors()[OSCoreOS]; !ok {
		t.Error("no sync error recorded")
	}
}
