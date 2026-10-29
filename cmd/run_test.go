package main

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/jeefy/booty/pkg/tftp"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/viper"
)

func setFlag(t *testing.T, key string, v any) {
	t.Helper()
	old := viper.Get(key)
	viper.Set(key, v)
	t.Cleanup(func() { viper.Set(key, old) })
}

func TestValidateFlagsDefaultsPass(t *testing.T) {
	if _, err := validateFlags(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

// --tftpBlockSize=0 keeps the pin/tftp default (512), as it did before the
// range check existed.
func TestValidateFlagsAcceptsDefaultTFTPBlockSize(t *testing.T) {
	config.LoadConfig()
	setFlag(t, config.TFTPBlockSize, 0)
	if _, err := validateFlags(); err != nil {
		t.Fatalf("--%s=0 must mean the library default: %v", config.TFTPBlockSize, err)
	}
}

func TestValidateFlagsRejects(t *testing.T) {
	for _, tc := range []struct {
		key  string
		val  any
		want string
	}{
		{config.TFTPBlockSize, 100, "--" + config.TFTPBlockSize},
		{config.TFTPBlockSize, 70000, "out of range"},
		{config.GithubAPIURL, "ftp://example.com", config.GithubAPIURL},
		{config.FedoraMirrorURL, "not a url", config.FedoraMirrorURL},
		{config.CoreOSStreamsURL, "https://example.com/streams.json", config.CoreOSStreamsURL},
		{config.JoinTokenTTL, "0s", "--" + config.JoinTokenTTL},
	} {
		t.Run(tc.key, func(t *testing.T) {
			setFlag(t, tc.key, tc.val)
			_, err := validateFlags()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s=%v: err = %v, want it to mention %q", tc.key, tc.val, err, tc.want)
			}
			if err := run(Cmd, nil); err == nil {
				t.Fatalf("%s=%v must fail startup", tc.key, tc.val)
			}
		})
	}
}

func TestRunVersionChecksStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ran []string
	checks := []versions.Job{
		{Name: "a", Fn: func() { ran = append(ran, "a"); cancel() }},
		{Name: "b", Fn: func() { ran = append(ran, "b") }},
	}
	runVersionChecks(ctx, checks)
	if strings.Join(ran, ",") != "a" {
		t.Fatalf("ran %v, want only a", ran)
	}
	runVersionChecks(context.Background(), checks)
	if strings.Join(ran, ",") != "a,a,b" {
		t.Fatalf("ran %v, want a then a,b", ran)
	}
}

func TestWaitBounded(t *testing.T) {
	var wg sync.WaitGroup
	if !waitBounded(&wg, time.Second, "test") {
		t.Fatal("an empty group must report done")
	}
	wg.Add(1)
	if waitBounded(&wg, 20*time.Millisecond, "test") {
		t.Fatal("a goroutine still running must time out")
	}
	wg.Done()
}

// A background loop's last hardware map update, made after cancel, must
// be in before shutdown flushes the hardware map and returns (the process
// exits right after): shutdown waits for the loops.
func TestShutdownWaitsForLoopsBeforeFlushingHardware(t *testing.T) {
	dir := t.TempDir()
	setFlag(t, config.DataDir, dir)
	setFlag(t, config.HardwareMap, "hardware.json")
	if err := hardware.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hardware.Close() })
	const mac = "aa:bb:cc:dd:ee:01"
	if _, err := hardware.Put(hardware.Host{MAC: mac}); err != nil {
		t.Fatal(err)
	}

	srv := &runningServers{http: &http.Server{}, errCh: make(chan error, 4)}
	var err error
	srv.tftp, err = tftp.Start(tftp.Config{Port: 0, DataDir: t.TempDir(), ServerHostPort: func() string { return "" }}, srv.errCh)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	bg := &background{}
	updateErr := make(chan error, 1)
	bg.goLoop(func() {
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond) // a loop finishing its last tick
		_, err := hardware.Update(mac, func(h *hardware.Host) { h.Running = "late" })
		updateErr <- err
	})
	cancel()
	if err := waitForShutdown(ctx, cancel, srv, bg); err != nil {
		t.Fatalf("clean shutdown returned %v", err)
	}
	select {
	case err := <-updateErr:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("waitForShutdown returned before the background loop finished")
	}

	db, err := hardware.Open(filepath.Join(dir, "hardware.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if h, ok := db.Get(mac); !ok || h.Running != "late" {
		t.Fatalf("late update lost on disk: %+v", h)
	}
}

func TestStartSchedulerFailureIsRecorded(t *testing.T) {
	t.Cleanup(func() { state.ClearSyncError(schedulerSyncError) })
	if s := startScheduler("not a cron spec", nil); s != nil {
		_ = s.Shutdown()
		t.Fatal("an invalid schedule must not start a scheduler")
	}
	got, ok := state.SyncErrors()[schedulerSyncError]
	if !ok || !strings.Contains(got.Error, "periodic checks disabled") || got.At.IsZero() {
		t.Fatalf("syncErrors[%q] = %+v, %v", schedulerSyncError, got, ok)
	}
}

func TestStartSchedulerSuccessClearsError(t *testing.T) {
	state.SetSyncError(schedulerSyncError, errors.New("stale"))
	t.Cleanup(func() { state.ClearSyncError(schedulerSyncError) })
	s := startScheduler("0 3 * * *", nil)
	if s == nil {
		t.Fatal("a valid schedule must start")
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.SyncErrors()[schedulerSyncError]; ok {
		t.Fatal("a started scheduler must clear the stale error")
	}
}
