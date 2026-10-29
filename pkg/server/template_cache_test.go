package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func TestIgnitionTemplateCachePicksUpEdits(t *testing.T) {
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	t.Cleanup(func() { viper.Set(config.DataDir, "") })
	path := filepath.Join(dir, "t.yaml")
	write := func(src string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now()
	write("variant: fcos\nversion: 1.5.0\n# {{ .Hostname }}\n", base)

	first, err := cachedIgnitionTemplate("t.yaml")
	if err != nil {
		t.Fatal(err)
	}
	again, err := cachedIgnitionTemplate("./t.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatal("an unchanged file must be served from the cache")
	}

	write("variant: fcos\nversion: 1.5.0\n# edited {{ .Hostname }}\n", base.Add(2*time.Second))
	edited, err := cachedIgnitionTemplate("t.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if edited == first || edited.src == first.src {
		t.Fatal("an edit on disk must be picked up")
	}

	write("{{ .Broken", base.Add(4*time.Second))
	if _, err := renderParsedIgnition(mustTemplate(t, "t.yaml"), &hardware.Host{}, ""); err == nil {
		t.Fatal("a template that does not parse must fail to render")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := cachedIgnitionTemplate("t.yaml"); err == nil {
		t.Fatal("a removed template must be an error, not a cached answer")
	}
}

func TestCachedTemplateRendersPerHost(t *testing.T) {
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	t.Cleanup(func() { viper.Set(config.DataDir, "") })
	src := "variant: fcos\nversion: 1.5.0\nstorage:\n  files:\n    - path: /etc/hostname\n      contents:\n        inline: \"{{ .Hostname }}\"\n"
	if err := os.WriteFile(filepath.Join(dir, "h.yaml"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl := mustTemplate(t, "h.yaml")
	a, err := renderParsedIgnition(tmpl, &hardware.Host{Hostname: "alpha"}, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := renderParsedIgnition(tmpl, &hardware.Host{Hostname: "beta"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(a) == string(b) {
		t.Fatal("per-host execution must not be cached")
	}
}

func mustTemplate(t *testing.T, name string) *parsedTemplate {
	t.Helper()
	tmpl, err := cachedIgnitionTemplate(name)
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

// A save through the template editor drops the cache entry: the handler's
// answer and the next render come from the saved file, never from a stale
// parse, and forgetCachedTemplate removes the entry outright.
func TestTemplateSaveInvalidatesCache(t *testing.T) {
	srv, _ := newTestServer(t)
	const name = "config/ignition.yaml"
	put := func(content string) templateResponse {
		t.Helper()
		b, err := json.Marshal(map[string]string{"name": name, "content": content})
		if err != nil {
			t.Fatal(err)
		}
		return decodeTemplate(t, do(t, http.MethodPut, srv.URL+"/config/template", string(b)))
	}
	put(minimalButane)
	key := templateCacheKey(viper.GetString(config.DataDir), name)
	entry := func() (templateCacheEntry, bool) {
		templateCache.Lock()
		defer templateCache.Unlock()
		e, ok := templateCache.m[key]
		return e, ok
	}
	e, ok := entry()
	if !ok {
		t.Fatal("loading the saved template must populate the cache")
	}
	stale := parseTemplate(name, "stale")
	templateCache.Lock()
	e.t = stale
	templateCache.m[key] = e
	templateCache.Unlock()

	updated := strings.Replace(minimalButane, "/etc/hostname", "/etc/booty-hostname", 1)
	if got := put(updated); got.Content != updated {
		t.Fatalf("PUT must answer with the saved template, got %q", got.Content)
	}
	if e, _ := entry(); e.t == stale || e.t.src != updated {
		t.Fatal("saving a template must replace its cache entry")
	}
	if tmpl := mustTemplate(t, name); tmpl.src != updated {
		t.Fatalf("render after save must use the saved template, got %q", tmpl.src)
	}

	forgetCachedTemplate(name)
	if _, ok := entry(); ok {
		t.Fatal("forgetCachedTemplate must drop the entry")
	}
}
