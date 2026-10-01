package actuator

import (
	"fmt"
	"log/slog"
	"syscall"
)

// SIGRTMIN as glibc defines it: the kernel's 32 plus the two signals NPTL
// reserves. systemd is built against glibc, so "SIGRTMIN+5" in
// systemd(1) is kernel signal 39 whatever libc the sender uses.
const glibcSIGRTMIN = 34

// RebootSignal is SIGRTMIN+5, which makes systemd start reboot.target;
// PowerOffSignal is SIGRTMIN+4, poweroff.target.
const (
	RebootSignal   = syscall.Signal(glibcSIGRTMIN + 5)
	PowerOffSignal = syscall.Signal(glibcSIGRTMIN + 4)
)

// NodeReboot is what `booty node-reboot` does inside the reboot Pod:
// flush the page cache and send RebootSignal to the host's PID 1 (the
// Pod runs with hostPID). pid and kill are parameters so a test can aim it
// at a child process.
func NodeReboot(pid int, sync func(), kill func(pid int, sig syscall.Signal) error) error {
	return signalInit(pid, sync, kill, RebootSignal, "SIGRTMIN+5", "systemd reboot.target", "Reboot requested")
}

// NodePowerOff is `booty node-reboot --poweroff`: the same, with
// PowerOffSignal.
func NodePowerOff(pid int, sync func(), kill func(pid int, sig syscall.Signal) error) error {
	return signalInit(pid, sync, kill, PowerOffSignal, "SIGRTMIN+4", "systemd poweroff.target", "Power-off requested")
}

func signalInit(pid int, sync func(), kill func(pid int, sig syscall.Signal) error, sig syscall.Signal, name, meaning, msg string) error {
	if sync == nil {
		sync = syscall.Sync
	}
	if kill == nil {
		kill = syscall.Kill
	}
	sync()
	if err := kill(pid, sig); err != nil {
		return fmt.Errorf("signalling pid %d with %s (%s): %w", pid, sig, name, err)
	}
	slog.Info(msg, "pid", pid, "signal", int(sig), "meaning", name+": "+meaning)
	return nil
}
