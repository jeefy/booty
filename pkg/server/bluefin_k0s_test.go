package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/cluster/k0s"
	"github.com/jeefy/booty/pkg/cluster/token"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestK0sManagedBluefinController(t *testing.T) {
	srv, m := newK0sServer(t, "managed")
	installBluefinFixture(t, viper.GetString(config.DataDir))
	registerK0sHosts(t, srv.URL)

	cfg, files, units := bluefinNode(t, srv.URL, k0sBluefinCP, "")
	if sc := files[k0s.BluefinSysconfig]; sc.contents != "K0S_CONTROLLER_ARGS=-c /etc/k0s/k0s.yaml --enable-worker --disable-components=helm,autopilot\n" || sc.mode != 0o644 || !sc.overwrite {
		t.Fatalf("sysconfig: %+v", sc)
	}
	for _, p := range []string{"/var/lib/k0s/pki/ca.crt", "/var/lib/k0s/pki/ca.key", "/var/lib/k0s/pki/sa.key", "/var/lib/k0s/pki/sa.pub", "/var/lib/k0s/pki/etcd/ca.crt", "/var/lib/k0s/pki/etcd/ca.key", k0s.ConfigPath, k0s.TokensManifest} {
		if f := files[p]; f.mode != 0o600 || !f.overwrite {
			t.Errorf("%s: %+v", p, f)
		}
	}
	if files["/var/lib/k0s/pki/ca.key"].contents != string(m.PKI.CAKey()) {
		t.Fatal("controller config carries the Manager's CA key")
	}
	if !strings.Contains(files[k0s.ConfigPath].contents, "externalAddress: 10.77.0.40") || !strings.Contains(files[k0s.ConfigPath].contents, "provider: kuberouter") {
		t.Fatalf("k0s.yaml:\n%s", files[k0s.ConfigPath].contents)
	}
	w, _ := m.Tokens.Peek(token.PurposeK0sWorker)
	if !strings.Contains(files[k0s.TokensManifest].contents, "bootstrap-token-"+w.ID()) {
		t.Fatal("tokens manifest")
	}
	if _, ok := files[k0s.TokenPath]; ok {
		t.Fatal("a controller has no /etc/k0s/token, so k0s-first-boot starts k0scontroller.service")
	}
	if u := units[k0s.UnitClusterReady]; u.Enabled == nil || !*u.Enabled || u.Contents == nil {
		t.Fatalf("ready unit: %+v", u)
	}
	if _, ok := units[k0s.ControllerUnit]; ok {
		t.Fatal("the k0s sysext ships k0scontroller.service; Booty must not replace it")
	}
	if f := files["/var/lib/k0s/k0s.raw"]; !f.remote || units["k0s-first-boot.service"].Name == "" {
		t.Fatalf("under --clusterDistribution=k0s every Bluefin host gets the k0s sysext: %+v", f)
	}
	if strings.Contains(files[k0s.ClusterReadyScript].contents, "/opt/bin/k0s") || len(cfg.Storage.Disks) != 0 {
		t.Fatal("bluefin runs the sysext's /usr/bin/k0s and touches no disk without stateDisk")
	}
	if r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+k0sBluefinCP+"&preview=1", ""); r.status != 200 || strings.Contains(r.body, "k0s") {
		t.Fatalf("the Flatcar/CoreOS builtin stays free of Bluefin k0s pieces: %+v", r)
	}
}

func TestK0sManagedBluefinWorker(t *testing.T) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		t.Skip("running inside a cluster")
	}
	srv, m := newK0sServer(t, "managed")
	installBluefinFixture(t, viper.GetString(config.DataDir))
	registerK0sHosts(t, srv.URL)

	_, files, units := bluefinNode(t, srv.URL, k0sBluefinW, "")
	tok := files[k0s.TokenPath]
	join, err := token.ParseK0s(strings.TrimSpace(tok.contents))
	w, _ := m.Tokens.Peek(token.PurposeK0sWorker)
	if err != nil || tok.mode != 0o600 || join.Server != "https://10.77.0.40:6443" || join.Token != w.Token || join.Role != k0s.Worker {
		t.Fatalf("worker token: %+v %+v %v", tok, join, err)
	}
	for p, f := range files {
		if strings.Contains(f.contents, "PRIVATE KEY") || p == k0s.BluefinSysconfig || p == k0s.ConfigPath {
			t.Fatalf("a worker gets only its token: %s", p)
		}
	}
	for name := range units {
		if name != "k0s-first-boot.service" {
			t.Fatalf("a worker needs no unit but k0s-first-boot.service: %v", units)
		}
	}
}

func TestK0sBluefinTwoControlPlanesWithoutEndpoint(t *testing.T) {
	srv, _ := newK0sServer(t, "managed")
	installBluefinFixture(t, viper.GetString(config.DataDir))
	register(t, srv.URL, `{"mac":"`+k0sBluefinCP+`","hostname":"bluefin-cp","os":"bluefin","role":"control-plane","stateDisk":"/dev/sdb"}`)
	register(t, srv.URL, `{"mac":"`+k0sFlatcarCP+`","hostname":"flatcar-cp","os":"flatcar","role":"control-plane"}`)
	viper.Set(config.ControlPlaneEndpt, "")
	clusterManager.Settings.Endpoint = ""
	_, files, _ := bluefinNode(t, srv.URL, k0sBluefinCP, "")
	if _, ok := files[k0s.ConfigPath]; ok {
		t.Fatal("two control planes without an endpoint: the node config is served without k0s cluster pieces")
	}
	if files["/etc/hostname"].contents != "bluefin-cp\n" {
		t.Fatal("the rest of the config still renders")
	}
}

func TestK0sExternalBluefin(t *testing.T) {
	srv, _ := newK0sServer(t, "external")
	installBluefinFixture(t, viper.GetString(config.DataDir))
	registerK0sHosts(t, srv.URL)
	if _, files, _ := bluefinNode(t, srv.URL, k0sBluefinW, ""); files[k0s.TokenPath].contents != "" {
		t.Fatal("no token source, no token")
	}

	tokenFile := filepath.Join(t.TempDir(), "token")
	tok, err := token.EncodeK0s(k0s.Worker, "k0s.example.org", []byte("-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n"), "abcdef.0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clusterManager.Settings.K0sTokenFile = tokenFile
	if _, files, _ := bluefinNode(t, srv.URL, k0sBluefinW, ""); files[k0s.TokenPath].contents != tok+"\n" {
		t.Fatal("external bluefin worker carries the file's token")
	}
	_, files, _ := bluefinNode(t, srv.URL, k0sBluefinCP, "")
	if _, ok := files[k0s.ConfigPath]; ok {
		t.Fatal("an external control-plane host gets no k0s cluster pieces")
	}
	if _, ok := files[k0s.BluefinSysconfig]; ok {
		t.Fatal("an external control-plane host gets no sysconfig")
	}
}

func TestK0sBluefinMintedTokenIsStableAcrossHeadAndGet(t *testing.T) {
	srv, m := newK0sServer(t, "managed")
	api, minter := newK0sFakeMinter(t)
	m.Minter = minter
	installBluefinFixture(t, viper.GetString(config.DataDir))
	register(t, srv.URL, `{"mac":"`+k0sBluefinW+`","hostname":"w-bluefin","os":"bluefin"}`)
	nodePath := "/bluefin/" + strings.ReplaceAll(k0sBluefinW, ":", "-") + "/bluefin-node.ign"

	if _, _, _ = bluefinNode(t, srv.URL, k0sBluefinW, "?preview=1"); api.posts.Load() != 0 {
		t.Fatal("a preview never mints")
	}
	resp := head(t, srv.URL+nodePath)
	if api.posts.Load() != 1 {
		t.Fatalf("the initrd's HEAD renders the config and mints once, posts=%d", api.posts.Load())
	}
	r := do(t, http.MethodGet, srv.URL+nodePath, "")
	if r.status != 200 || resp.ContentLength != int64(len(r.body)) || api.posts.Load() != 1 {
		t.Fatalf("the GET after the HEAD reuses the cached token (same bytes): %d vs %d posts=%d", resp.ContentLength, len(r.body), api.posts.Load())
	}
	_, files, _ := bluefinNode(t, srv.URL, k0sBluefinW, "")
	join, err := token.ParseK0s(strings.TrimSpace(files[k0s.TokenPath].contents))
	pre, _ := m.Tokens.Peek(token.PurposeK0sWorker)
	if err != nil || join.Token == pre.Token || join.Server != "https://10.77.0.40:6443" || api.posts.Load() != 1 {
		t.Fatalf("the config carries the minted token: %+v %v posts=%d", join, err, api.posts.Load())
	}
}
