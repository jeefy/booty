package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	ign "github.com/jeefy/booty/pkg/ignition"
	"github.com/spf13/viper"
)

func TestBluefinNodeCarriesBootyUnits(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)

	_, files, units := bluefinNodeAll(t, srv.URL, bluefinMAC, "?preview=1")
	for _, name := range bluefinBootyUnits {
		u, ok := units[name]
		if !ok || u.Contents == nil {
			t.Fatalf("%s missing from the node config: %v", name, units)
		}
		if enabled := u.Enabled != nil && *u.Enabled; enabled == (name == ign.UpdateServiceName) {
			t.Fatalf("%s enabled=%v", name, enabled)
		}
	}
	if !strings.Contains(*units[ign.BootedUnitName].Contents, "http://192.168.1.10:8080/booted?mac=$$MAC") {
		t.Fatalf("booted unit: %s", *units[ign.BootedUnitName].Contents)
	}
	if !strings.Contains(*units[ign.UpdateServiceName].Contents, "ExecStart="+bluefinUpdateCheckScript+"\n") || !strings.Contains(*units[ign.HealthUnitName].Contents, "ExecStart="+bluefinHealthReportScript+"\n") {
		t.Fatalf("units must run the /etc/booty scripts: %v", units)
	}
	for _, p := range bluefinBootyFiles {
		f, ok := files[p]
		if !ok || f.mode != 0o755 || !f.overwrite || !strings.HasPrefix(f.contents, "#!/bin/bash\n") {
			t.Fatalf("%s: %+v", p, f)
		}
	}
	if !strings.Contains(files[bluefinUpdateCheckScript].contents, `"http://192.168.1.10:8080/update-check"`) || !strings.Contains(files[bluefinHealthReportScript].contents, `"http://192.168.1.10:8080/health?mac=$MAC"`) {
		t.Fatalf("scripts must talk to Booty: %v", files)
	}

	viper.Set(config.Builtin, "hostname,health")
	_, files, units = bluefinNodeAll(t, srv.URL, bluefinMAC, "?preview=1")
	if _, ok := units[ign.HealthUnitName]; !ok || len(units) != 1 || len(files) != 2 {
		t.Fatalf("--builtin toggles apply to the node config: %v %v", units, files)
	}
	viper.Set(config.Builtin, "none")
	if r := do(t, http.MethodGet, srv.URL+bluefinNodePath+"?preview=1", ""); r.status != 404 {
		t.Fatalf("--builtin=none with nothing else to configure is 404: %+v", r)
	}
}
