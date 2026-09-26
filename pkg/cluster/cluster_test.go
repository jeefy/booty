package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/cluster/pki"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func defaults() Settings {
	return Settings{
		Distribution: Kubeadm,
		ControlPlane: External,
		CNI:          Cilium,
		PodCIDR:      config.DefaultPodCIDR,
		ServiceCIDR:  config.DefaultServiceCIDR,
		KubeadmJoin:  config.KubeadmJoinStatic,
	}
}

func TestFromConfigReadsDefaults(t *testing.T) {
	viper.Reset()
	config.LoadConfig()
	s := FromConfig()
	if s.Distribution != Kubeadm || s.ControlPlane != External || s.CNI != Cilium || s.PodCIDR != "10.244.0.0/16" || s.ServiceCIDR != "10.96.0.0/12" {
		t.Fatalf("defaults: %+v", s)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	if s.Managed() {
		t.Fatal("default control plane is external")
	}
}

func TestValidate(t *testing.T) {
	caDir := t.TempDir()
	if _, err := pki.LoadOrCreate(caDir); err != nil {
		t.Fatal(err)
	}
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*Settings)
		want   string
	}{
		{"ok", func(*Settings) {}, ""},
		{"distribution", func(s *Settings) { s.Distribution = "rke2" }, "--clusterDistribution"},
		{"mode", func(s *Settings) { s.ControlPlane = "shared" }, "--controlPlane"},
		{"cni", func(s *Settings) { s.CNI = "weave" }, "--cni"},
		{"podCIDR", func(s *Settings) { s.PodCIDR = "10.244.0.0" }, "--podCIDR"},
		{"serviceCIDR", func(s *Settings) { s.ServiceCIDR = "nope" }, "--serviceCIDR"},
		{"endpoint URL", func(s *Settings) { s.Endpoint = "https://10.0.0.1:6443" }, "--controlPlaneEndpoint"},
		{"endpoint bad port", func(s *Settings) { s.Endpoint = "10.0.0.1:99999" }, "--controlPlaneEndpoint"},
		{"endpoint host", func(s *Settings) { s.Endpoint = "cp.example.org" }, ""},
		{"endpoint host:port", func(s *Settings) { s.Endpoint = "10.0.0.1:6443" }, ""},
		{"endpoint ipv6", func(s *Settings) { s.Endpoint = "[fd00::1]:6443" }, ""},
		{"profile with k0s", func(s *Settings) { s.Profile = "kubeadm-worker"; s.Distribution = K0s }, "conflicts"},
		{"profile with kubeadm", func(s *Settings) { s.Profile = "kubeadm-worker" }, ""},
		{"k0s", func(s *Settings) { s.Distribution = K0s }, ""},
		{"managed missing CA dir", func(s *Settings) { s.ControlPlane = Managed; s.CADir = filepath.Join(caDir, "missing") }, "cluster CA"},
		{"managed CA dir is a file", func(s *Settings) { s.ControlPlane = Managed; s.CADir = filepath.Join(caDir, "ca.crt") }, "cluster CA"},
		{"managed CA dir ok", func(s *Settings) { s.ControlPlane = Managed; s.CADir = caDir }, ""},
		{"external ignores CA dir", func(s *Settings) { s.CADir = "/nonexistent" }, ""},
		{"kubeconfig missing", func(s *Settings) { s.Kubeconfig = "/nonexistent/kubeconfig" }, "--kubeconfig"},
		{"kubeconfig ok", func(s *Settings) { s.Kubeconfig = kubeconfig }, ""},
		{"cp disk ok", func(s *Settings) { s.ControlPlaneDisk = "/dev/vda" }, ""},
		{"cp disk not /dev", func(s *Settings) { s.ControlPlaneDisk = "vda" }, "--controlPlaneDisk"},
		{"cp disk traversal", func(s *Settings) { s.ControlPlaneDisk = "/dev/../etc" }, "--controlPlaneDisk"},
		{"cp disk equals containerd disk", func(s *Settings) { s.ControlPlaneDisk, s.ContainerdDisk = "/dev/vda", "/dev/vda" }, "must be different devices"},
		{"cp and containerd disks differ", func(s *Settings) { s.ControlPlaneDisk, s.ContainerdDisk = "/dev/vda", "/dev/vdb" }, ""},
	}
	for _, tc := range cases {
		s := defaults()
		tc.mutate(&s)
		err := s.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: want error containing %q, got %v", tc.name, tc.want, err)
		}
	}
}

func TestSupports(t *testing.T) {
	for _, tc := range []struct {
		os   string
		dist Distribution
		want bool
	}{
		{"flatcar", Kubeadm, true}, {"flatcar", K0s, true},
		{"coreos", Kubeadm, true}, {"coreos", K0s, true},
		{"", Kubeadm, true}, {"", K0s, true},
		{"bluefin", Kubeadm, false}, {"bluefin", K0s, true},
		{"windows", Kubeadm, false},
	} {
		if got := Supports(tc.os, tc.dist); got != tc.want {
			t.Errorf("Supports(%q,%q)=%v want %v", tc.os, tc.dist, got, tc.want)
		}
	}
}

func TestNewExternalHasNoPKI(t *testing.T) {
	viper.Set(config.DataDir, t.TempDir())
	m, err := New(defaults())
	if err != nil {
		t.Fatal(err)
	}
	if m.PKI != nil {
		t.Fatal("external control plane must not create a CA")
	}
	if m.Tokens == nil || m.Tokens.Path() != config.ClusterPath(config.ClusterTokensFile) {
		t.Fatalf("token store %+v", m.Tokens)
	}
	if _, err := os.Stat(config.ClusterPath(config.ClusterPKIDir)); !os.IsNotExist(err) {
		t.Fatalf("pki dir must not exist for external, err=%v", err)
	}
}

func TestNewManagedCreatesOwnCA(t *testing.T) {
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	s := defaults()
	s.ControlPlane = Managed
	m, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	if m.PKI == nil || m.PKI.Dir() != filepath.Join(dir, "cluster", "pki") {
		t.Fatalf("pki %+v", m.PKI)
	}
	info, err := os.Stat(filepath.Join(dir, "cluster", "pki"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("pki dir mode %v err=%v", info.Mode(), err)
	}
	again, err := New(s)
	if err != nil || again.PKI.Fingerprint() != m.PKI.Fingerprint() {
		t.Fatalf("second start must reuse the CA: %v", err)
	}
}

func TestNewManagedBYODirIsReadOnly(t *testing.T) {
	viper.Set(config.DataDir, t.TempDir())
	src, err := pki.LoadOrCreate(filepath.Join(t.TempDir(), "src"))
	if err != nil {
		t.Fatal(err)
	}
	byo := t.TempDir()
	for name, data := range map[string][]byte{pki.CACertFile: src.CACert(), pki.CAKeyFile: src.CAKey()} {
		if err := os.WriteFile(filepath.Join(byo, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := defaults()
	s.ControlPlane, s.CADir = Managed, byo
	m, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	if m.PKI.Fingerprint() != src.Fingerprint() {
		t.Fatal("BYO CA must be the one loaded")
	}
	entries, _ := os.ReadDir(byo)
	if len(entries) != 2 {
		t.Fatalf("BYO dir must stay untouched, has %d entries", len(entries))
	}

	s.Distribution = K0s
	if _, err := New(s); err == nil || !strings.Contains(err.Error(), "sa.key") || !strings.Contains(err.Error(), "never writes") {
		t.Fatalf("k0s with a kubeadm-only BYO dir must fail, got %v", err)
	}
}

func TestEndpointAndWarnings(t *testing.T) {
	viper.Set(config.DataDir, t.TempDir())
	s := defaults()
	s.ControlPlane, s.ControlPlaneDisk = Managed, "/dev/vda"
	m, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	hosts := map[string]*hardware.Host{}
	if _, err := m.Endpoint(hosts); err != ErrNoControlPlane {
		t.Fatalf("no hosts: %v", err)
	}
	if w := m.Warnings(hosts); len(w) != 1 || w[0] != "no control-plane host registered" {
		t.Fatalf("warnings %v", w)
	}

	hosts["aa:bb:cc:dd:ee:01"] = &hardware.Host{MAC: "aa:bb:cc:dd:ee:01", OS: "flatcar", Role: hardware.RoleControlPlane}
	if _, err := m.Endpoint(hosts); err == nil || !strings.Contains(err.Error(), "no IP") {
		t.Fatalf("CP without IP: %v", err)
	}
	hosts["aa:bb:cc:dd:ee:01"].IP = "192.168.1.20"
	if ep, err := m.Endpoint(hosts); err != nil || ep != "192.168.1.20" {
		t.Fatalf("single CP: %q %v", ep, err)
	}
	if w := m.Warnings(hosts); len(w) != 0 {
		t.Fatalf("warnings %v", w)
	}

	hosts["aa:bb:cc:dd:ee:02"] = &hardware.Host{MAC: "aa:bb:cc:dd:ee:02", OS: "bluefin", Role: hardware.RoleControlPlane, IP: "192.168.1.21"}
	if _, err := m.Endpoint(hosts); err == nil || !strings.Contains(err.Error(), "2 control-plane hosts") {
		t.Fatalf("two CPs: %v", err)
	}
	w := m.Warnings(hosts)
	if len(w) != 2 || !strings.Contains(w[0], "2 control-plane hosts") || w[1] != "host aa:bb:cc:dd:ee:02 (bluefin) unsupported under kubeadm" {
		t.Fatalf("warnings %v", w)
	}

	m.Settings.Endpoint = "vip.example.org:6443"
	if ep, err := m.Endpoint(hosts); err != nil || ep != "vip.example.org:6443" {
		t.Fatalf("flag wins: %q %v", ep, err)
	}
	if w := m.Warnings(hosts); len(w) != 1 {
		t.Fatalf("with the endpoint set only the bluefin warning remains: %v", w)
	}

	if RoleOf(hosts["aa:bb:cc:dd:ee:01"]) != ControlPlane || RoleOf(&hardware.Host{}) != Worker {
		t.Fatal("RoleOf")
	}
}

func TestWarningsControlPlaneDisk(t *testing.T) {
	viper.Set(config.DataDir, t.TempDir())
	s := defaults()
	s.ControlPlane, s.Endpoint = Managed, "10.0.0.1"
	m, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	hosts := map[string]*hardware.Host{
		"aa:bb:cc:dd:ee:01": {MAC: "aa:bb:cc:dd:ee:01", OS: "flatcar", Role: hardware.RoleControlPlane},
	}
	w := m.Warnings(hosts)
	if len(w) != 1 || w[0] != "host aa:bb:cc:dd:ee:01 (flatcar): control-plane host needs --controlPlaneDisk on a PXE-booted OS" {
		t.Fatalf("warnings %v", w)
	}
	m.Settings.ControlPlaneDisk = "/dev/vda"
	if w := m.Warnings(hosts); len(w) != 0 {
		t.Fatalf("with a disk: %v", w)
	}
	m.Settings.ControlPlaneDisk, m.Settings.Distribution = "", K0s
	if w := m.Warnings(hosts); len(w) != 0 {
		t.Fatalf("k0s does not need the disk yet: %v", w)
	}
	for os, want := range map[string]bool{"": true, "flatcar": true, "coreos": true, "bluefin": false} {
		if got := NeedsControlPlaneDisk(os); got != want {
			t.Errorf("NeedsControlPlaneDisk(%q)=%v", os, got)
		}
	}
}
