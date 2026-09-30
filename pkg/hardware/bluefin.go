package hardware

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Bluefin Server boot modes. An empty Mode is diskless.
const (
	ModeDiskless  = "diskless"
	ModeInstalled = "installed"
)

// Firmware a Bluefin host netbooted through, recorded in NetbootPlatform.
const (
	PlatformEFI    = "efi"
	PlatformPCBIOS = "pcbios"
)

// Bluefin Server opt-in sysexts a host can list in Extensions.
const (
	ExtensionZFS                    = "zfs"
	ExtensionKubeStellar            = "kubestellar"
	ExtensionK0s                    = "k0s"
	ExtensionNvidiaContainerToolkit = "nvidia-container-toolkit"
)

// ValidExtensions are the fixed sysext names Extensions accepts; NVIDIA
// driver flavours (IsNvidiaDriverFlavour) are accepted on top of them.
var ValidExtensions = []string{ExtensionK0s, ExtensionKubeStellar, ExtensionNvidiaContainerToolkit, ExtensionZFS}

// nvidiaDriverFlavour matches an NVIDIA open-kernel-module driver sysext,
// nvidia-open-<driver branch>, so a new branch needs no code change here.
var nvidiaDriverFlavour = regexp.MustCompile(`^nvidia-open-[0-9]{1,6}$`)

// IsNvidiaDriverFlavour reports whether name is an NVIDIA driver flavour
// such as nvidia-open-595.
func IsNvidiaDriverFlavour(name string) bool {
	return nvidiaDriverFlavour.MatchString(name)
}

var (
	ErrInvalidStateDisk  = errors.New("invalid stateDisk")
	ErrInvalidExtensions = errors.New("invalid extensions")
	ErrInvalidMode       = errors.New("invalid mode")
	ErrBluefinOnly       = errors.New("only valid for os bluefin")
)

// ValidateStateDisk accepts an empty value or an absolute /dev path, like
// ValidateInstallDisk.
func ValidateStateDisk(disk string) error {
	return validateDevicePath(ErrInvalidStateDisk, disk)
}

// NormalizeExtensions trims, deduplicates and sorts names and checks them
// against ValidExtensions and the NVIDIA driver flavours. kubestellar runs
// on k0s and requires it; a host takes at most one driver flavour, and not
// alongside zfs: both sysexts ship the kernel's module index, so merging
// both hides one set of modules (the driver sysext refuses to load then).
func NormalizeExtensions(names []string) ([]string, error) {
	var out, flavours []string
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		driver := IsNvidiaDriverFlavour(n)
		if !driver && !slices.Contains(ValidExtensions, n) {
			return nil, fmt.Errorf("%w: %q is not one of %s or nvidia-open-<branch>", ErrInvalidExtensions, n, strings.Join(ValidExtensions, ", "))
		}
		if slices.Contains(out, n) {
			continue
		}
		out = append(out, n)
		if driver {
			flavours = append(flavours, n)
		}
	}
	slices.Sort(out)
	slices.Sort(flavours)
	if slices.Contains(out, ExtensionKubeStellar) && !slices.Contains(out, ExtensionK0s) {
		return nil, fmt.Errorf("%w: %s requires %s", ErrInvalidExtensions, ExtensionKubeStellar, ExtensionK0s)
	}
	if len(flavours) > 1 {
		return nil, fmt.Errorf("%w: only one NVIDIA driver flavour per host, got %s", ErrInvalidExtensions, strings.Join(flavours, ", "))
	}
	if len(flavours) == 1 && slices.Contains(out, ExtensionZFS) {
		return nil, fmt.Errorf("%w: %s and %s cannot be merged on one host (both ship the kernel module index)", ErrInvalidExtensions, flavours[0], ExtensionZFS)
	}
	return out, nil
}

// ValidateMode accepts an empty mode, ModeDiskless or ModeInstalled.
func ValidateMode(mode string) error {
	switch mode {
	case "", ModeDiskless, ModeInstalled:
		return nil
	}
	return fmt.Errorf("%w %q: must be empty, %q or %q", ErrInvalidMode, mode, ModeDiskless, ModeInstalled)
}

// ValidateBluefinFields normalizes and checks StateDisk, Extensions and
// Mode. They are refused on other operating systems, and the state disk
// must not be the disk an install wipes.
func ValidateBluefinFields(h *Host) error {
	h.StateDisk = strings.TrimSpace(h.StateDisk)
	h.Mode = strings.TrimSpace(h.Mode)
	exts, err := NormalizeExtensions(h.Extensions)
	if err != nil {
		return err
	}
	h.Extensions = exts
	switch h.NetbootPlatform {
	case "", PlatformEFI, PlatformPCBIOS:
	default:
		return fmt.Errorf("%w %q: netbootPlatform must be empty, %q or %q", ErrInvalidMode, h.NetbootPlatform, PlatformEFI, PlatformPCBIOS)
	}
	if h.OS != "bluefin" {
		h.NetbootPlatform = ""
		for field, set := range map[string]bool{"stateDisk": h.StateDisk != "", "extensions": len(h.Extensions) > 0, "mode": h.Mode != ""} {
			if set {
				return fmt.Errorf("%s: %w", field, ErrBluefinOnly)
			}
		}
		return nil
	}
	if err := ValidateStateDisk(h.StateDisk); err != nil {
		return err
	}
	if err := ValidateMode(h.Mode); err != nil {
		return err
	}
	if h.StateDisk != "" && h.StateDisk == h.InstallDisk {
		return fmt.Errorf("%w %q: must differ from installDisk, which an install wipes", ErrInvalidStateDisk, h.StateDisk)
	}
	return nil
}

// Installed reports whether h is a Bluefin host that boots from its
// installed disk.
func (h *Host) Installed() bool {
	return h != nil && h.Mode == ModeInstalled
}

// NetbootsBIOS reports whether h's last Bluefin netboot was a legacy BIOS
// one, which cannot install (that needs UEFI and systemd-boot).
func (h *Host) NetbootsBIOS() bool {
	return h != nil && h.NetbootPlatform == PlatformPCBIOS
}

// HasExtension reports whether h opted into the sysext name.
func (h *Host) HasExtension(name string) bool {
	return h != nil && slices.Contains(h.Extensions, name)
}
