package hardware

import (
	"errors"
	"slices"
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
	if (&Host{OS: "bluefin"}).Installed() {
		t.Fatal("empty mode is diskless")
	}
}
