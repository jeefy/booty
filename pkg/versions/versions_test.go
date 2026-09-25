package versions

import (
	"strings"
	"testing"
)

func TestParseFlatcarDigests(t *testing.T) {
	const sha512 = "ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{
			name: "sha512 section present after sha1",
			in: "# SHA1 HASH\n" +
				"0123456789abcdef0123456789abcdef01234567  flatcar_production_pxe.vmlinuz\n" +
				"# SHA512 HASH\n" +
				sha512 + "  flatcar_production_pxe.vmlinuz\n",
			want: strings.ToLower(sha512),
			ok:   true,
		},
		{
			name: "sha512 section only with surrounding whitespace",
			in:   "\n  # SHA512 HASH  \n  " + sha512 + "  file\n",
			want: strings.ToLower(sha512),
			ok:   true,
		},
		{
			name: "sha1 only",
			in: "# SHA1 HASH\n" +
				"0123456789abcdef0123456789abcdef01234567  flatcar_production_pxe.vmlinuz\n",
			ok: false,
		},
		{
			name: "sha512 header followed by another header",
			in:   "# SHA512 HASH\n# SHA1 HASH\nabc  file\n",
			ok:   false,
		},
		{
			name: "empty",
			in:   "",
			ok:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFlatcarDigests(strings.NewReader(tc.in))
			if tc.ok != (err == nil) {
				t.Fatalf("ok=%v err=%v", tc.ok, err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

const streamsFixture = `{
  "stream": "stable",
  "architectures": {
    "x86_64": {
      "artifacts": {
        "metal": {
          "release": "39.20231101.3.0",
          "formats": {
            "pxe": {
              "kernel": {"location": "https://example/kernel", "sha256": "aaa111"},
              "initramfs": {"location": "https://example/initramfs", "sha256": "bbb222"},
              "rootfs": {"location": "https://example/rootfs", "sha256": "ccc333"}
            }
          }
        }
      }
    },
    "aarch64": {
      "artifacts": {
        "metal": {
          "release": "39.20231101.3.0",
          "formats": {
            "pxe": {
              "kernel": {"location": "https://example/kernel-arm"}
            }
          }
        }
      }
    }
  }
}`

func TestExtractCoreOSChecksum(t *testing.T) {
	body := []byte(streamsFixture)
	tests := []struct {
		arch, artifact, want string
		ok                   bool
	}{
		{"x86_64", "kernel", "aaa111", true},
		{"x86_64", "initramfs", "bbb222", true},
		{"x86_64", "rootfs", "ccc333", true},
		{"x86_64", "bogus", "", false},
		{"aarch64", "kernel", "", false},
		{"ppc64le", "kernel", "", false},
	}
	for _, tc := range tests {
		got, err := extractCoreOSChecksum(body, tc.arch, tc.artifact)
		if tc.ok != (err == nil) {
			t.Errorf("%s/%s: ok=%v err=%v", tc.arch, tc.artifact, tc.ok, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s/%s: got %q want %q", tc.arch, tc.artifact, got, tc.want)
		}
	}
	if _, err := extractCoreOSChecksum(nil, "x86_64", "kernel"); err == nil {
		t.Error("nil body should error")
	}
}

func TestCoreOSArtifactNames(t *testing.T) {
	names := coreOSArtifactNames("39.20231101.3.0", "x86_64")
	if names["kernel"] != "fedora-coreos-39.20231101.3.0-live-kernel-x86_64" {
		t.Fatalf("unexpected kernel name %q", names["kernel"])
	}
	if names["rootfs"] != "fedora-coreos-39.20231101.3.0-live-rootfs.x86_64.img" {
		t.Fatalf("unexpected rootfs name %q", names["rootfs"])
	}
}
