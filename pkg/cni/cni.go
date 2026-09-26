// Package cni renders the oneshot that installs the cluster network plugin
// from the first kubeadm control plane: a pinned release per plugin, the
// script that applies it with the node's admin kubeconfig, and the systemd
// unit that retries until it succeeds and then leaves a marker so later
// boots skip it.
package cni

import (
	"fmt"
	"net"
	"strings"
)

// Plugin names, matching the --cni flag.
const (
	Cilium  = "cilium"
	Calico  = "calico"
	Flannel = "flannel"
	None    = "none"
)

// Pinned releases, checked against the GitHub releases API on 2026-09-25.
const (
	// https://github.com/cilium/cilium/releases/tag/v1.20.2
	CiliumRelease = "v1.20.2"
	// https://github.com/cilium/cilium-cli/releases/tag/v0.20.1 and the
	// cilium-linux-amd64.tar.gz.sha256sum asset next to the tarball.
	CiliumCLIRelease = "v0.20.1"
	CiliumCLISHA256  = "24e817dcfcc8a12e325ce7547617bcbcf171c5b0b335bb24d2bb1207ba047f61"
	// https://github.com/projectcalico/calico/releases/tag/v3.32.2
	CalicoRelease = "v3.32.2"
	// https://github.com/flannel-io/flannel/releases/tag/v0.28.9
	FlannelRelease = "v0.28.9"

	// FlannelDefaultPodCIDR is the Network baked into upstream
	// kube-flannel.yml; the apply script rewrites it for other --podCIDR
	// values.
	FlannelDefaultPodCIDR = "10.244.0.0/16"
)

// Paths and unit names on the control-plane host.
const (
	UnitName   = "booty-cni-apply.service"
	ScriptPath = "/opt/booty/cni-apply.sh"
	// MarkerPath lives on the control-plane disk (bind-mounted state), so a
	// PXE reboot does not re-apply the manifests.
	MarkerPath = "/var/lib/booty-cp/cni-applied"
	Kubeconfig = "/etc/kubernetes/admin.conf"
)

// Install is the rendered unit and script for one plugin.
type Install struct {
	Name    string
	Release string
	Unit    string
	Script  string
}

// Pin returns the pinned release of a plugin; empty for none/unknown.
func Pin(name string) string {
	switch name {
	case Cilium:
		return CiliumRelease
	case Calico:
		return CalicoRelease
	case Flannel:
		return FlannelRelease
	}
	return ""
}

// Render returns the unit and script installing name at release (the pin
// when empty) for a cluster whose pods live in podCIDR. after is the unit
// the install waits for (kubeadm init). It returns nil for None.
func Render(name, release, podCIDR, after string) (*Install, error) {
	if name == None {
		return nil, nil
	}
	if release == "" {
		release = Pin(name)
	}
	if release == "" {
		return nil, fmt.Errorf("unknown CNI %q", name)
	}
	if _, _, err := net.ParseCIDR(podCIDR); err != nil {
		return nil, fmt.Errorf("pod CIDR %q: %w", podCIDR, err)
	}
	if strings.ContainsAny(release, " \t\r\n\"'`$\\") {
		return nil, fmt.Errorf("CNI release %q contains shell metacharacters", release)
	}
	var body string
	switch name {
	case Cilium:
		body = ciliumScript(release)
	case Calico:
		body = calicoScript(release, podCIDR)
	case Flannel:
		body = flannelScript(release, podCIDR)
	default:
		return nil, fmt.Errorf("unknown CNI %q", name)
	}
	return &Install{
		Name:    name,
		Release: release,
		Unit:    unit(name, release, after),
		Script:  scriptHeader + body + scriptFooter,
	}, nil
}

func unit(name, release, after string) string {
	return `[Unit]
Description=Install the ` + name + ` ` + release + ` network plugin from this control plane
Requires=` + after + `
After=` + after + `
ConditionPathExists=!` + MarkerPath + `

[Service]
Type=oneshot
RemainAfterExit=yes
Restart=on-failure
RestartSec=30s
Environment=KUBECONFIG=` + Kubeconfig + `
ExecStart=` + ScriptPath + `

[Install]
WantedBy=multi-user.target
`
}

const scriptHeader = `#!/bin/bash
set -euo pipefail
export PATH=/opt/bin:$PATH KUBECONFIG=` + Kubeconfig + `
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
wait_for() { # wait_for <seconds> <command...>: retry until it succeeds
  local deadline=$(( $(date +%s) + $1 )); shift
  until "$@"; do
    if [ "$(date +%s)" -ge "$deadline" ]; then echo "timed out waiting for: $*" >&2; return 1; fi
    sleep 5
  done
}
wait_for 300 kubectl get --raw /readyz >/dev/null 2>&1
`

const scriptFooter = `mkdir -p "$(dirname ` + MarkerPath + `)" && touch ` + MarkerPath + `
echo "CNI applied"
`

func ciliumScript(release string) string {
	return `CLI_VERSION="` + CiliumCLIRelease + `"
CLI_SHA256="` + CiliumCLISHA256 + `"
CILIUM_VERSION="` + release + `"
if [ ! -x /opt/bin/cilium ]; then
  curl -fsSL -o "$TMP/cilium.tgz" "https://github.com/cilium/cilium-cli/releases/download/${CLI_VERSION}/cilium-linux-amd64.tar.gz"
  echo "${CLI_SHA256}  $TMP/cilium.tgz" | sha256sum -c -
  mkdir -p /opt/bin && tar -C /opt/bin -xzf "$TMP/cilium.tgz" cilium
fi
# ipam.mode=kubernetes hands out the podCIDR ranges kubeadm assigned to each node.
if ! kubectl -n kube-system get daemonset cilium >/dev/null 2>&1; then
  cilium install --version "${CILIUM_VERSION}" --set ipam.mode=kubernetes
fi
cilium status --wait --wait-duration 10m
`
}

func calicoScript(release, podCIDR string) string {
	return `CALICO_VERSION="` + release + `"
POD_CIDR="` + podCIDR + `"
# The operator bundle is too large for client-side apply's annotation.
kubectl apply --server-side --force-conflicts -f "https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VERSION}/manifests/tigera-operator.yaml"
kubectl wait --for=condition=established --timeout=120s crd/installations.operator.tigera.io crd/apiservers.operator.tigera.io
cat > "$TMP/calico.yaml" <<EOF
apiVersion: operator.tigera.io/v1
kind: Installation
metadata:
  name: default
spec:
  calicoNetwork:
    ipPools:
      - name: default-ipv4-ippool
        blockSize: 26
        cidr: ${POD_CIDR}
        encapsulation: VXLANCrossSubnet
        natOutgoing: Enabled
        nodeSelector: all()
---
apiVersion: operator.tigera.io/v1
kind: APIServer
metadata:
  name: default
spec: {}
EOF
kubectl apply -f "$TMP/calico.yaml"
wait_for 120 kubectl -n calico-system get daemonset calico-node >/dev/null 2>&1
kubectl -n calico-system rollout status daemonset/calico-node --timeout=10m
`
}

func flannelScript(release, podCIDR string) string {
	return `FLANNEL_VERSION="` + release + `"
POD_CIDR="` + podCIDR + `"
curl -fsSL -o "$TMP/kube-flannel.yml" "https://github.com/flannel-io/flannel/releases/download/${FLANNEL_VERSION}/kube-flannel.yml"
if [ "${POD_CIDR}" != "` + FlannelDefaultPodCIDR + `" ]; then
  # Upstream hard-codes the Network in net-conf.json; substitute it, but only
  # when the manifest still looks the way this script expects.
  if [ "$(grep -c '"Network": "` + FlannelDefaultPodCIDR + `"' "$TMP/kube-flannel.yml")" != 1 ]; then
    echo "kube-flannel.yml ${FLANNEL_VERSION} does not carry exactly one Network entry; refusing to patch it" >&2
    exit 1
  fi
  sed -i "s#\"Network\": \"` + FlannelDefaultPodCIDR + `\"#\"Network\": \"${POD_CIDR}\"#" "$TMP/kube-flannel.yml"
fi
kubectl apply -f "$TMP/kube-flannel.yml"
kubectl -n kube-flannel rollout status daemonset/kube-flannel-ds --timeout=10m
`
}
