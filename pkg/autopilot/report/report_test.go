package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fixture is a report stub laced with everything that must not survive:
// the hostnames aren and ehrlitan, MACs, IPs, UUIDs, a /bluefin/<mac>/
// URL, an SSH key and a token.
func fixture() Input {
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	return Input{
		OS: "bluefin", Version: "26.09.700", LastGood: "26.09.673", Mode: "full", Class: "failed-units",
		RollbackResult: "lastGood 26.09.673 healthy on the same hardware (aren, 192.168.1.57)",
		DMIHash:        "9f86d081884c7d65", Draft: false, CreatedAt: t0, UpdatedAt: t0.Add(90 * time.Minute),
		Attempts: []Attempt{
			{Attempt: 1, Target: "26.09.700", Outcome: "failed", Class: "failed-units", T0: t0, Ended: t0.Add(4 * time.Minute),
				Note:        "1 failed unit(s): booty-kubeadm-join.service on aren",
				FailedUnits: []string{"booty-kubeadm-join.service"},
				Timeline:    []string{"t+0s kernel fetched", "t+38s ignition fetched", "t+2m10s OS up (booted)", "t+3m1s health reported: 1 failed unit(s)"}},
			{Attempt: 2, Target: "26.09.700", Outcome: "failed", Class: "failed-units", T0: t0.Add(10 * time.Minute), Ended: t0.Add(14 * time.Minute),
				FailedUnits: []string{"booty-kubeadm-join.service"}, Timeline: []string{"t+0s kernel fetched", "t+40s ignition fetched"}},
			{Attempt: 3, Target: "26.09.673", Outcome: "healthy", T0: t0.Add(20 * time.Minute), Ended: t0.Add(30 * time.Minute), Note: "node Ready, workloads healthy (node aren, 10.0.0.7)"},
		},
		Hardware: Hardware{Vendor: "HP", Product: "HP EliteDesk 800 G1 DM", BIOSVersion: "L01 v02.78", Firmware: "uefi", Kernel: "6.17.1-300.fc44.x86_64", BootPath: BootPathUEFIHTTP},
		Node:     Node{KubeletVersion: "v1.34.3", OSImage: "Bluefin Server 26.09.700", ContainerRuntimeVersion: "containerd://2.1.4", KernelVersion: "6.17.1-300.fc44.x86_64"},
		CNI:      CNI{Name: "cilium", Version: "v1.20.2"},
		JournalErrors: []string{
			"2026-09-28T12:02:11+0000 systemd[1]: booty-kubeadm-join.service: Failed with result 'exit-code'.",
			"2026-09-28T12:02:10+0000 kubeadm-join.sh[812]: error execution phase preflight: couldn't validate the identity of the API Server: Get \"https://192.168.1.10:6443/api/v1/namespaces/kube-public/configmaps/cluster-info?timeout=10s\": dial tcp 192.168.1.10:6443: connect: no route to host",
			"2026-09-28T12:02:09+0000 kubeadm-join.sh[812]: kubeadm join 192.168.1.10:6443 --token abcdef.0123456789abcdef --discovery-token-ca-cert-hash sha256:deadbeef --node-name aren",
			"2026-09-28T12:01:50+0000 systemd-pull[400]: Pulling http://192.168.1.10:8080/bluefin/40:a8:f0:12:34:56/bluefin-server_26.09.700.raw for 40:a8:f0:12:34:56 (ehrlitan)",
			"2026-09-28T12:01:40+0000 NetworkManager[600]: <warn> device (enp0s31f6): hw addr 40:A8:F0:12:34:56, ip6 fe80::42a8:f0ff:fe12:3456%enp0s31f6, gw 192.168.1.1",
			"2026-09-28T12:01:30+0000 kernel: DMI: HP HP EliteDesk 800 G1 DM/18E7, BIOS L01 v02.78 12/01/2020 product_uuid=4c4c4544-0031-3310-8052-b6c04f4d3732 boot_id=5b1c2f3e-4d5a-6b7c-8d9e-0f1a2b3c4d5e",
			"2026-09-28T12:01:20+0000 sshd[700]: Accepted publickey for root from 192.168.1.20 port 51234 ssh2: ED25519 SHA256:abc key ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGVzdGVzdGtleXRlc3RrZXl0ZXN0a2V5dGVzdGtleXQ",
			"2026-09-28T12:01:10+0000 curl[650]: POST http://booty.lan/health?mac=aa%3Abb%3Acc%3Add%3Aee%3A01&token=s3cr3tvalue failed",
			"2026-09-28T12:01:00+0000 hostnamectl[500]: hostname set to aren (was ehrlitan.lan)",
		},
		Names: []string{"aren", "ehrlitan"},
	}
}

var piiPattern = regexp.MustCompile(`([0-9a-f]{2}:){5}|[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+|aren|ehrlitan|[0-9a-f]{8}-[0-9a-f]{4}-`)

func TestBuildAndWriteRedactsEverything(t *testing.T) {
	in := fixture()
	rep := Build(in)
	dir := filepath.Join(t.TempDir(), "reports")
	key := Key(in.OS, in.Version)
	if key != "bluefin-26.09.700" || !ValidKey(key) {
		t.Fatalf("key %q", key)
	}
	if err := Write(dir, key, rep); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir, key) {
		t.Fatal("both files must exist")
	}
	md, js := Paths(dir, key)
	for _, path := range []string{md, js} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if m := piiPattern.FindAllString(string(data), -1); len(m) != 0 {
			t.Fatalf("%s leaks identifiers: %q", filepath.Base(path), m)
		}
		for _, needle := range []string{"AAAAC3", "s3cr3tvalue", "4c4c4544", "5b1c2f3e-", "fe80", "40-a8", "abcdef.0123456789abcdef"} {
			if strings.Contains(string(data), needle) {
				t.Fatalf("%s contains %q", filepath.Base(path), needle)
			}
		}
		if !strings.Contains(string(data), "booty-autopilot: bluefin 26.09.700 9f86d081884c7d65") {
			t.Fatalf("%s lacks the dedupe marker", filepath.Base(path))
		}
	}

	mdText := rep.Markdown
	for _, want := range []string{
		"<!-- booty-autopilot: bluefin 26.09.700 9f86d081884c7d65 -->",
		"# Bluefin Server 26.09.700: failed-units (autopilot report)",
		"| lastGood | `26.09.673` |",
		"| Boot path | `uefi-http` |",
		"| Hardware | HP HP EliteDesk 800 G1 DM |",
		"| BIOS | L01 v02.78 |",
		"| Kernel | 6.17.1-300.fc44.x86_64 |",
		"| kubelet | v1.34.3 |",
		"| Node osImage | Bluefin Server 26.09.700 |",
		"| CNI | cilium v1.20.2 |",
		"| Attempts | 3 |",
		"lastGood 26.09.673 healthy on the same hardware (<host>, <ip>)",
		"### Attempt 1 into `26.09.700`: failed (`failed-units`)",
		"### Attempt 3 into `26.09.673`: healthy",
		"- `booty-kubeadm-join.service`",
		"t+38s ignition fetched",
		"/bluefin/<host>/bluefin-server_26.09.700.raw for <mac> (<host>)",
		"hw addr <mac>, ip6 <ip>, gw <ip>",
		"product_uuid=<uuid> boot_id=<uuid>",
		"ssh-ed25519 <key>",
		"mac=<mac>&token=<token>",
		"hostname set to <host> (was <host>.lan)",
		"Status: **quarantined**",
	} {
		if !strings.Contains(mdText, want) {
			t.Errorf("markdown lacks %q\n%s", want, mdText)
		}
	}

	var doc Document
	data, _ := os.ReadFile(js)
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Marker != Marker("bluefin", "26.09.700", "9f86d081884c7d65") || doc.AttemptCount != 3 || len(doc.JournalErrors) != 9 || doc.Draft || doc.CNI == nil || doc.CNI.Name != "cilium" {
		t.Fatalf("json doc: %+v", doc)
	}
	if len(doc.FailedUnits) != 1 || doc.FailedUnits[0] != "booty-kubeadm-join.service" {
		t.Fatalf("failed units: %v", doc.FailedUnits)
	}
	if rep.Document() != rep.JSON {
		t.Fatal("Document accessor")
	}
}

func TestBuildDraftWithoutOptionalFacts(t *testing.T) {
	rep := Build(Input{OS: "flatcar", Version: "4800.0.0", Draft: true, Class: "boot-loop"})
	md := rep.Markdown
	for _, want := range []string{"# Flatcar 4800.0.0: boot-loop", "draft (release in TIMEOUT", "| Boot path | `unknown` |", "<!-- booty-autopilot: flatcar 4800.0.0 unknown -->", "_none recorded_", "never got as far as a health report (`boot-loop`)"} {
		if !strings.Contains(md, want) {
			t.Errorf("draft markdown lacks %q\n%s", want, md)
		}
	}
	if md := Build(Input{OS: "flatcar", Version: "4800.0.0", Draft: true, Class: "failed-units"}).Markdown; !strings.Contains(md, "_no error-level journal lines were reported_") {
		t.Errorf("a boot that reported health without journal errors says so:\n%s", md)
	}
	if strings.Contains(md, "| CNI |") || strings.Contains(md, "| kubelet |") {
		t.Fatalf("optional rows must be omitted:\n%s", md)
	}
	doc := rep.Document()
	if doc.JournalErrors == nil || doc.FailedUnits == nil || doc.Attempts == nil {
		t.Fatalf("json lists must not be null: %+v", doc)
	}
}

func TestWriteRefusesBadKeys(t *testing.T) {
	for _, key := range []string{"", "../x", "bluefin-..", "Bluefin-1", "flatcar-v1", "flatcar", "flatcar-1/2"} {
		if ValidKey(key) {
			t.Errorf("ValidKey(%q) must be false", key)
		}
		if err := Write(t.TempDir(), key, Build(Input{})); err == nil {
			t.Errorf("Write(%q) must fail", key)
		}
	}
	for _, key := range []string{"flatcar-4757.2.0", "coreos-44.20260913.2.1", "bluefin-26.09.673"} {
		if !ValidKey(key) {
			t.Errorf("ValidKey(%q) must be true", key)
		}
	}
}

func TestMarkerHelpers(t *testing.T) {
	if got := MarkerPrefix("bluefin", "26.09.673"); got != "booty-autopilot: bluefin 26.09.673" {
		t.Fatal(got)
	}
	if got := Marker("bluefin", "26.09.673", "abc"); got != "<!-- booty-autopilot: bluefin 26.09.673 abc -->" {
		t.Fatal(got)
	}
	if got := MarkerText("bluefin", "1", ""); got != "booty-autopilot: bluefin 1 unknown" {
		t.Fatal(got)
	}
}
