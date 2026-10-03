package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeefy/booty/pkg/versions"
)

func TestStorageEndpoint(t *testing.T) {
	srv, dir := newTestServer(t)
	writeRelease(t, dir, "flatcar", "4757.2.0", "flatcar_production_pxe.vmlinuz", "flatcar_production_pxe_image.cpio.gz")
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"f1","os":"flatcar","targetVersion":"4757.2.0"}`)
	versions.InvalidateStorage()

	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/storage", ""), http.StatusMethodNotAllowed)
	r := do(t, http.MethodGet, srv.URL+"/storage", "")
	if r.status != 200 {
		t.Fatalf("storage: %+v", r)
	}
	var s struct {
		DataDir string `json:"dataDir"`
		Total   struct {
			Bytes int64 `json:"bytes"`
		} `json:"total"`
		Free struct {
			Bytes int64 `json:"bytes"`
		} `json:"free"`
		OS map[string]struct {
			Tracked  bool   `json:"tracked"`
			Channel  string `json:"channel"`
			Bytes    int64  `json:"bytes"`
			Releases []struct {
				Version  string   `json:"version"`
				Bytes    int64    `json:"bytes"`
				Files    int      `json:"files"`
				Modified string   `json:"modified"`
				Links    []string `json:"links"`
				Hosts    struct {
					Running []string `json:"running"`
					Pinned  []string `json:"pinned"`
				} `json:"hosts"`
				Retained string `json:"retained"`
				Cached   bool   `json:"cached"`
			} `json:"releases"`
		} `json:"os"`
		Assets []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"assets"`
		Autopilot struct {
			StateBytes   int64 `json:"stateBytes"`
			ReportsBytes int64 `json:"reportsBytes"`
		} `json:"autopilot"`
	}
	if err := json.Unmarshal([]byte(r.body), &s); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	if s.DataDir != dir || s.Total.Bytes <= 0 || s.Free.Bytes <= 0 {
		t.Fatalf("totals: %s", r.body)
	}
	fl := s.OS["flatcar"]
	if !fl.Tracked || len(fl.Releases) != 1 {
		t.Fatalf("flatcar: %+v", fl)
	}
	rel := fl.Releases[0]
	if rel.Version != "4757.2.0" || rel.Files != 2 || rel.Bytes == 0 || !rel.Cached || rel.Retained != "pinned" || len(rel.Links) != 0 || len(rel.Hosts.Pinned) != 1 || rel.Hosts.Pinned[0] != "aa:bb:cc:dd:ee:01" || len(rel.Hosts.Running) != 0 {
		t.Fatalf("release: %+v", rel)
	}
	for _, osName := range []string{"coreos", "bluefin"} {
		if b, ok := s.OS[osName]; !ok || b.Releases == nil {
			t.Fatalf("%s block must be present with an empty release list: %s", osName, r.body)
		}
	}
	kinds := map[string]string{}
	for _, a := range s.Assets {
		kinds[a.Name] = a.Kind
	}
	if kinds["flatcar_pin.txt"] != "other" || kinds["hardware.json"] != "other" {
		t.Fatalf("assets: %v", kinds)
	}
	if _, listed := kinds["kernel.tmp"]; !listed {
		t.Fatalf("every top-level regular file is listed: %v", kinds)
	}
}
