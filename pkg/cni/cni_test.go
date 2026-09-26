package cni

import (
	"strings"
	"testing"
)

func TestRenderNoneIsNil(t *testing.T) {
	got, err := Render(None, "", "10.244.0.0/16", "booty-k8s-init.service")
	if err != nil || got != nil {
		t.Fatalf("none must render nothing: %+v %v", got, err)
	}
}

func TestRenderErrors(t *testing.T) {
	for _, tc := range []struct{ name, release, cidr string }{
		{"weave", "", "10.244.0.0/16"},
		{Cilium, "", "10.244.0.0"},
		{Cilium, `v1.0; rm -rf /`, "10.244.0.0/16"},
		{Flannel, "$(id)", "10.244.0.0/16"},
	} {
		if _, err := Render(tc.name, tc.release, tc.cidr, "x.service"); err == nil {
			t.Errorf("Render(%q,%q,%q) must fail", tc.name, tc.release, tc.cidr)
		}
	}
}

func TestPins(t *testing.T) {
	for name, want := range map[string]string{Cilium: CiliumRelease, Calico: CalicoRelease, Flannel: FlannelRelease, None: "", "weave": ""} {
		if got := Pin(name); got != want {
			t.Errorf("Pin(%q)=%q want %q", name, got, want)
		}
	}
	for _, pin := range []string{CiliumRelease, CiliumCLIRelease, CalicoRelease, FlannelRelease} {
		if !strings.HasPrefix(pin, "v") {
			t.Errorf("pin %q must be a v-tag", pin)
		}
	}
	if len(CiliumCLISHA256) != 64 {
		t.Fatalf("cilium-cli sha256 %q", CiliumCLISHA256)
	}
}

func TestUnitShape(t *testing.T) {
	for _, name := range []string{Cilium, Calico, Flannel} {
		in, err := Render(name, "", "10.244.0.0/16", "booty-k8s-init.service")
		if err != nil {
			t.Fatal(err)
		}
		if in.Name != name || in.Release != Pin(name) {
			t.Fatalf("%s: %+v", name, in)
		}
		for _, want := range []string{
			"Requires=booty-k8s-init.service\n", "After=booty-k8s-init.service\n",
			"ConditionPathExists=!" + MarkerPath, "Restart=on-failure", "RestartSec=30s",
			"Environment=KUBECONFIG=/etc/kubernetes/admin.conf", "ExecStart=" + ScriptPath,
			"Type=oneshot", "RemainAfterExit=yes", "WantedBy=multi-user.target",
		} {
			if !strings.Contains(in.Unit, want) {
				t.Errorf("%s unit missing %q:\n%s", name, want, in.Unit)
			}
		}
		for _, want := range []string{"#!/bin/bash\nset -euo pipefail", "KUBECONFIG=/etc/kubernetes/admin.conf", "touch " + MarkerPath, in.Release} {
			if !strings.Contains(in.Script, want) {
				t.Errorf("%s script missing %q:\n%s", name, want, in.Script)
			}
		}
	}
}

func TestCiliumScript(t *testing.T) {
	in, err := Render(Cilium, "", "10.244.0.0/16", "x.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`CLI_VERSION="` + CiliumCLIRelease + `"`, `CLI_SHA256="` + CiliumCLISHA256 + `"`, `CILIUM_VERSION="` + CiliumRelease + `"`,
		"cilium-cli/releases/download/${CLI_VERSION}/cilium-linux-amd64.tar.gz", "sha256sum -c -",
		`cilium install --version "${CILIUM_VERSION}" --set ipam.mode=kubernetes`, "cilium status --wait",
	} {
		if !strings.Contains(in.Script, want) {
			t.Errorf("cilium script missing %q", want)
		}
	}
	custom, err := Render(Cilium, "v1.19.0", "10.244.0.0/16", "x.service")
	if err != nil || !strings.Contains(custom.Script, `CILIUM_VERSION="v1.19.0"`) || !strings.Contains(custom.Script, `CLI_VERSION="`+CiliumCLIRelease+`"`) {
		t.Fatalf("--cniRelease must override cilium but keep the cli pin: %v\n%s", err, custom.Script)
	}
}

func TestCalicoScript(t *testing.T) {
	in, err := Render(Calico, "", "10.100.0.0/16", "x.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"projectcalico/calico/${CALICO_VERSION}/manifests/tigera-operator.yaml", "kubectl apply --server-side",
		"kind: Installation", "cidr: ${POD_CIDR}", `POD_CIDR="10.100.0.0/16"`, "kind: APIServer",
		"rollout status daemonset/calico-node",
	} {
		if !strings.Contains(in.Script, want) {
			t.Errorf("calico script missing %q", want)
		}
	}
}

func TestFlannelScript(t *testing.T) {
	in, err := Render(Flannel, "", "10.244.0.0/16", "x.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"flannel-io/flannel/releases/download/${FLANNEL_VERSION}/kube-flannel.yml",
		`if [ "${POD_CIDR}" != "10.244.0.0/16" ]`, `grep -c '"Network": "10.244.0.0/16"'`, "sed -i",
		"rollout status daemonset/kube-flannel-ds",
	} {
		if !strings.Contains(in.Script, want) {
			t.Errorf("flannel script missing %q", want)
		}
	}
}
