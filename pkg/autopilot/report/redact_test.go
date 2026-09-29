package report

import (
	"strings"
	"testing"
)

func TestRedactRules(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"ipv4", "connect to 192.168.1.57 failed", "connect to <ip> failed"},
		{"ipv4 cidr", "podCIDR 10.244.0.0/16", "podCIDR <ip>"},
		{"ipv4 with port", "dial tcp 10.0.0.1:6443: refused", "dial tcp <ip>:6443: refused"},
		{"version is not an ip", "flatcar 4757.2.0 and bluefin 26.09.673", "flatcar 4757.2.0 and bluefin 26.09.673"},
		{"ipv6 full", "src 2001:0db8:85a3:0000:0000:8a2e:0370:7334 dst", "src <ip> dst"},
		{"ipv6 compressed", "listening on 2001:db8::1", "listening on <ip>"},
		{"ipv6 loopback", "bound to ::1", "bound to <ip>"},
		{"ipv6 link local zoned", "via fe80::1a2b:3c4d:5e6f:7a8b%enp0s31f6 dev", "via <ip> dev"},
		{"ipv6 bracketed with port", "url https://[fe80::1%eth0]:6443/readyz", "url https://<ip>:6443/readyz"},
		{"ipv6 mapped ipv4", "peer ::ffff:192.168.1.5", "peer <ip>"},
		{"time is not ipv6", "2026-09-28T10:00:01+0000 kernel: started at 10:00:01", "2026-09-28T10:00:01+0000 kernel: started at 10:00:01"},
		{"mac colons", "link 40:a8:f0:12:34:56 up", "link <mac> up"},
		{"mac upper", "hw AA:BB:CC:DD:EE:01", "hw <mac>"},
		{"mac dashes", "client 40-a8-f0-12-34-56", "client <mac>"},
		{"mac query", "GET /ignition.json?mac=aa:bb:cc:dd:ee:01", "GET /ignition.json?mac=<mac>"},
		{"mac query encoded", "GET /ignition/user.json?mac=aa%3Abb%3Acc%3Add%3Aee%3A01", "GET /ignition/user.json?mac=<mac>"},
		{"bluefin dir", "pull http://booty.lan/bluefin/40-a8-f0-12-34-56/bluefin-server_26.09.673.raw", "pull http://booty.lan/bluefin/<host>/bluefin-server_26.09.673.raw"},
		{"bluefin dir colons", "HEAD /bluefin/40:a8:f0:12:34:56/bluefin-node.ign", "HEAD /bluefin/<host>/bluefin-node.ign"},
		{"uuid", "product_uuid 4c4c4544-0031-3310-8052-b6c04f4d3732", "product_uuid <uuid>"},
		{"boot id", "boot 5b1c2f3e4d5a6b7c8d9e0f1a2b3c4d5e is not a uuid; 5b1c2f3e-4d5a-6b7c-8d9e-0f1a2b3c4d5e is", "boot 5b1c2f3e4d5a6b7c8d9e0f1a2b3c4d5e is not a uuid; <uuid> is"},
		{"ssh key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGVzdGVzdGtleXRlc3RrZXl0ZXN0a2V5dGVzdGtleXQ core@aren", "ssh-ed25519 <key> core@aren"},
		{"bearer", "Authorization: Bearer ghp_abcdefghijklmnopqrstuvwxyz0123456789", "Authorization: Bearer <token>"},
		{"bearer opaque", "hdr bearer eyJhbGciOiJSUzI1NiIsImtpZCI6In0.eyJpc3MiOiJrdWJlIn0.sig", "hdr bearer <token>"},
		{"token query", "join https://cp:6443?token=abcdef.0123456789abcdef&x=1", "join https://cp:6443?token=<token>&x=1"},
		{"discovery token flag", "kubeadm join --token abcdef.0123456789abcdef --discovery-token-ca-cert-hash sha256:dead", "kubeadm join --token <token> --discovery-token-ca-cert-hash sha256:dead"},
		{"token file flag", "k0s worker --token-file=/etc/k0s/token", "k0s worker --token-file=<token>"},
		{"github pat", "using github_pat_11ABCDEFG0123456789abcdefghijklmnopqrstuvwxyz", "using <token>"},
		{"verbatim", "kubelet.service: Failed with result 'exit-code'.", "kubelet.service: Failed with result 'exit-code'."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Redact(tc.in); got != tc.want {
				t.Fatalf("Redact(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactNames(t *testing.T) {
	names := []string{"aren", "ehrlitan", "n1", "x", ""}
	cases := []struct{ in, want string }{
		{"aren kernel: oops", "<host> kernel: oops"},
		{"node aren.lan NotReady", "node <host>.lan NotReady"},
		{"Aren and EHRLITAN and aren", "<host> and <host> and <host>"},
		{"warens is not aren-2", "warens is not aren-2"},
		{"kubelet[1]: n1 registered", "kubelet[1]: <host> registered"},
		{"x marks nothing", "x marks nothing"},
		{"node=aren,ip=192.168.1.5", "node=<host>,ip=<ip>"},
	}
	for _, tc := range cases {
		if got := Redact(tc.in, names...); got != tc.want {
			t.Fatalf("Redact(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
	if got := RedactAll([]string{"aren", "1.2.3.4"}, "aren"); strings.Join(got, ",") != "<host>,<ip>" {
		t.Fatalf("RedactAll: %v", got)
	}
	if RedactAll(nil) != nil {
		t.Fatal("RedactAll(nil) must stay nil")
	}
}
