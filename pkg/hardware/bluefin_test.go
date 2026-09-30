package hardware

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeExtensions(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
		err  error
	}{
		{name: "empty", in: nil, want: nil},
		{name: "sorted and deduplicated", in: []string{" zfs", "k0s", "ZFS", ""}, want: []string{"k0s", "zfs"}},
		{name: "kubestellar with k0s", in: []string{"kubestellar", "k0s"}, want: []string{"k0s", "kubestellar"}},
		{name: "kubestellar without k0s", in: []string{"kubestellar"}, err: ErrInvalidExtensions},
		{name: "unknown", in: []string{"docker"}, err: ErrInvalidExtensions},
		{name: "nvidia driver flavour", in: []string{"NVIDIA-open-595"}, want: []string{"nvidia-open-595"}},
		{name: "any nvidia driver branch", in: []string{"nvidia-open-615"}, want: []string{"nvidia-open-615"}},
		{name: "nvidia container toolkit alone", in: []string{"nvidia-container-toolkit"}, want: []string{"nvidia-container-toolkit"}},
		{name: "driver with toolkit and k0s", in: []string{"nvidia-container-toolkit", "nvidia-open-595", "k0s", "nvidia-open-595"}, want: []string{"k0s", "nvidia-container-toolkit", "nvidia-open-595"}},
		{name: "two driver flavours", in: []string{"nvidia-open-595", "nvidia-open-615"}, err: ErrInvalidExtensions},
		{name: "driver with zfs and toolkit", in: []string{"zfs", "nvidia-open-595", "nvidia-container-toolkit"}, want: []string{"nvidia-container-toolkit", "nvidia-open-595", "zfs"}},
		{name: "driver without branch", in: []string{"nvidia-open-"}, err: ErrInvalidExtensions},
		{name: "driver with a non-numeric branch", in: []string{"nvidia-open-latest"}, err: ErrInvalidExtensions},
		{name: "proprietary driver", in: []string{"nvidia-595"}, err: ErrInvalidExtensions},
		{name: "versioned driver file name", in: []string{"nvidia-open-595_20260927.123"}, err: ErrInvalidExtensions},
		{name: "bare nvidia", in: []string{"nvidia"}, err: ErrInvalidExtensions},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeExtensions(tc.in)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestNormalizeExtensionsNvidiaErrors(t *testing.T) {
	for in, want := range map[string][]string{
		"only one NVIDIA driver flavour per host, got nvidia-open-595, nvidia-open-615":                         {"nvidia-open-615", "nvidia-open-595"},
		`"nvidia-open-x" is not one of k0s, kubestellar, nvidia-container-toolkit, zfs or nvidia-open-<branch>`: {"nvidia-open-x"},
	} {
		if _, err := NormalizeExtensions(want); err == nil || !strings.Contains(err.Error(), in) {
			t.Errorf("%v: err = %v, want it to contain %q", want, err, in)
		}
	}
}

func TestValidateBluefinFields(t *testing.T) {
	cases := []struct {
		name string
		host Host
		err  error
	}{
		{name: "bluefin all set", host: Host{OS: "bluefin", StateDisk: "/dev/sda", Extensions: []string{"zfs"}, Mode: ModeInstalled, InstallDisk: "/dev/nvme0n1"}},
		{name: "bluefin diskless", host: Host{OS: "bluefin", Mode: ModeDiskless}},
		{name: "bad mode", host: Host{OS: "bluefin", Mode: "usb"}, err: ErrInvalidMode},
		{name: "relative state disk", host: Host{OS: "bluefin", StateDisk: "sda"}, err: ErrInvalidStateDisk},
		{name: "state disk is install disk", host: Host{OS: "bluefin", StateDisk: "/dev/sda", InstallDisk: "/dev/sda"}, err: ErrInvalidStateDisk},
		{name: "flatcar with state disk", host: Host{OS: "flatcar", StateDisk: "/dev/sda"}, err: ErrBluefinOnly},
		{name: "default os with extensions", host: Host{Extensions: []string{"zfs"}}, err: ErrBluefinOnly},
		{name: "coreos with mode", host: Host{OS: "coreos", Mode: ModeDiskless}, err: ErrBluefinOnly},
		{name: "flatcar untouched", host: Host{OS: "flatcar", InstallDisk: "/dev/sda"}},
		{name: "bluefin bios platform", host: Host{OS: "bluefin", NetbootPlatform: PlatformPCBIOS}},
		{name: "bad platform", host: Host{OS: "bluefin", NetbootPlatform: "arm"}, err: ErrInvalidMode},
		{name: "flatcar platform is cleared", host: Host{OS: "flatcar", NetbootPlatform: PlatformEFI}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.host
			err := ValidateBluefinFields(&h)
			if tc.err == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
		})
	}
}

func TestHostInstalledAndHasExtension(t *testing.T) {
	h := &Host{OS: "bluefin", Mode: ModeInstalled, Extensions: []string{"k0s"}}
	if !h.Installed() || !h.HasExtension("k0s") || h.HasExtension("zfs") {
		t.Fatalf("unexpected: %+v", h)
	}
	var none *Host
	if none.Installed() || none.HasExtension("k0s") {
		t.Fatal("nil host must report false")
	}
	if !(&Host{NetbootPlatform: PlatformPCBIOS}).NetbootsBIOS() || (&Host{NetbootPlatform: PlatformEFI}).NetbootsBIOS() || (&Host{}).NetbootsBIOS() {
		t.Fatal("NetbootsBIOS")
	}
	if (&Host{OS: "bluefin"}).Installed() {
		t.Fatal("empty mode is diskless")
	}
}
