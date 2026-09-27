package rpm

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/rpm/rpmtest"
)

var testMembers = []rpmtest.Member{
	{Name: "./etc/dnf/protected.d/shim.conf", Mode: 0o100644, Data: []byte("shim-x64\n")},
	{Name: "./usr/lib/efi/shim/16.1-7/EFI/fedora", Mode: 0o040755},
	{Name: "./usr/lib/efi/shim/16.1-7/EFI/fedora/shimx64.efi", Mode: 0o100700, Data: bytes.Repeat([]byte("MZshim"), 100)},
	{Name: "./usr/lib/efi/shim/16.1-7/EFI/fedora/mmx64.efi", Mode: 0o100700, Data: []byte("MZmok")},
	{Name: "./usr/lib/efi/shim/16.1-7/EFI/BOOT/BOOTX64.EFI", Mode: 0o120777, Data: []byte("../fedora/shimx64.efi")},
}

func mustBuild(t *testing.T, compressor string, tags map[int32]string, payload []byte) []byte {
	t.Helper()
	pkg, err := rpmtest.Build(compressor, tags, payload)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestPayloadParsesHeaderTags(t *testing.T) {
	rpm := mustBuild(t, "zstd", map[int32]string{rpmtest.TagPayloadFormat: "cpio", rpmtest.TagPayloadCompressor: "zstd", 1000: "shim-x64"}, rpmtest.Newc(testMembers...))
	payload, h, err := Payload(bytes.NewReader(rpm))
	if err != nil {
		t.Fatal(err)
	}
	if h.PayloadFormat != "cpio" || h.PayloadCompressor != "zstd" {
		t.Errorf("header = %+v", h)
	}
	cr := NewCpioReader(payload)
	var names []string
	for {
		hdr, err := cr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
		if hdr.Name == "./usr/lib/efi/shim/16.1-7/EFI/fedora" && !hdr.Mode.IsDir() {
			t.Errorf("directory mode = %v", hdr.Mode)
		}
		if strings.HasSuffix(hdr.Name, "BOOTX64.EFI") && hdr.Mode&os.ModeSymlink == 0 {
			t.Errorf("symlink mode = %v", hdr.Mode)
		}
	}
	if len(names) != len(testMembers) {
		t.Errorf("members = %v", names)
	}
}

func TestExtractZstdAndGzip(t *testing.T) {
	for _, algo := range []string{"zstd", "gzip"} {
		t.Run(algo, func(t *testing.T) {
			rpm := mustBuild(t, algo, map[int32]string{rpmtest.TagPayloadFormat: "cpio", rpmtest.TagPayloadCompressor: algo}, rpmtest.Newc(testMembers...))
			got, err := Extract(bytes.NewReader(rpm), "usr/lib/efi/shim/16.1-7/EFI/fedora/shimx64.efi", "/usr/lib/efi/shim/16.1-7/EFI/fedora/mmx64.efi", "usr/lib/efi/shim/*/EFI/fedora/shimx64.efi")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got["usr/lib/efi/shim/*/EFI/fedora/shimx64.efi"], testMembers[2].Data) {
				t.Error("wildcard member not extracted")
			}
			if !bytes.Equal(got["usr/lib/efi/shim/16.1-7/EFI/fedora/shimx64.efi"], testMembers[2].Data) {
				t.Errorf("shimx64.efi = %q", got["usr/lib/efi/shim/16.1-7/EFI/fedora/shimx64.efi"])
			}
			if string(got["/usr/lib/efi/shim/16.1-7/EFI/fedora/mmx64.efi"]) != "MZmok" {
				t.Errorf("mmx64.efi = %q", got["/usr/lib/efi/shim/16.1-7/EFI/fedora/mmx64.efi"])
			}
		})
	}
}

func TestExtractMissingMember(t *testing.T) {
	rpm := mustBuild(t, "zstd", map[int32]string{rpmtest.TagPayloadCompressor: "zstd"}, rpmtest.Newc(testMembers...))
	_, err := Extract(bytes.NewReader(rpm), "usr/lib/efi/shim/16.1-7/EFI/fedora/shimx64.efi", "usr/lib/efi/grub2/grubx64.efi")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "grubx64.efi") {
		t.Fatalf("err = %v", err)
	}
	_, err = Extract(bytes.NewReader(rpm), "usr/lib/efi/shim/16.1-7/EFI/BOOT/BOOTX64.EFI")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("symlink member must not count as found: %v", err)
	}
	_, err = Extract(bytes.NewReader(rpm), "usr/lib/efi/shim/16.1-7/EFI/fedora/*.efi")
	if err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("ambiguous wildcard must fail: %v", err)
	}
}

func TestPayloadRejectsUnsupported(t *testing.T) {
	cases := map[string][]byte{
		"xz compressor":  mustBuild(t, "", map[int32]string{rpmtest.TagPayloadCompressor: "xz"}, []byte("xz")),
		"not cpio":       mustBuild(t, "zstd", map[int32]string{rpmtest.TagPayloadFormat: "drpm", rpmtest.TagPayloadCompressor: "zstd"}, []byte("x")),
		"bad lead magic": append([]byte{1, 2, 3, 4}, make([]byte, 200)...),
		"truncated":      mustBuild(t, "zstd", map[int32]string{rpmtest.TagPayloadCompressor: "zstd"}, nil)[:leadSize+10],
	}
	for name, rpm := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Payload(bytes.NewReader(rpm)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	badMagic := rpmtest.Newc(testMembers[0])
	copy(badMagic, "070707")
	if _, err := NewCpioReader(bytes.NewReader(badMagic)).Next(); err == nil {
		t.Fatal("cpio reader accepted odc magic")
	}
}

func TestExtractRealFedoraShim(t *testing.T) {
	path := "/tmp/opencode/sb/shim-x64-16.1-7.x86_64.rpm"
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("real Fedora shim rpm not available: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	got, err := Extract(f, "usr/lib/efi/shim/16.1-7/EFI/fedora/shimx64.efi")
	if err != nil {
		t.Fatal(err)
	}
	efi := got["usr/lib/efi/shim/16.1-7/EFI/fedora/shimx64.efi"]
	if len(efi) != 1036008 || string(efi[:2]) != "MZ" {
		t.Errorf("shimx64.efi: %d bytes, magic %q", len(efi), efi[:2])
	}
}
