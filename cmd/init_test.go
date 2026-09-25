package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/ignition"
	"github.com/spf13/viper"
)

func TestRunInit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	detect := func() (string, error) { return "192.168.1.10", nil }

	var out bytes.Buffer
	if err := runInit(&out, dir, detect); err != nil {
		t.Fatal(err)
	}
	butane, err := os.ReadFile(filepath.Join(dir, "config", "ignition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(butane) != ignition.StarterButane {
		t.Fatal("starter butane must be written verbatim")
	}
	hw, err := os.ReadFile(filepath.Join(dir, "hardware.json"))
	if err != nil || string(hw) != "{}\n" {
		t.Fatalf("hardware.json = %q, %v", hw, err)
	}
	for _, want := range []string{
		"ignition.yaml created", "hardware.json created",
		"next-server 192.168.1.10", "undionly.kpxe", "ipxe.efi",
		"booty --dataDir " + dir + " --serverIP 192.168.1.10",
		"http://192.168.1.10:8080/",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "hardware.json"), []byte(`{"aa:bb:cc:dd:ee:01":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runInit(&out, dir, detect); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "exists, skipped") != 2 || strings.Contains(out.String(), "created") {
		t.Fatalf("second run must skip both files:\n%s", out.String())
	}
	hw, _ = os.ReadFile(filepath.Join(dir, "hardware.json"))
	if string(hw) != `{"aa:bb:cc:dd:ee:01":{}}` {
		t.Fatal("existing files must never be overwritten")
	}

	out.Reset()
	if err := runInit(&out, filepath.Join(t.TempDir(), "x"), func() (string, error) { return "", errors.New("no route") }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "next-server <serverIP>") || !strings.Contains(out.String(), "no route") {
		t.Fatalf("detection failure must be reported with a placeholder:\n%s", out.String())
	}
}

func TestStartupValidation(t *testing.T) {
	t.Cleanup(func() {
		viper.Set(config.AutoRegister, "")
		viper.Set(config.HostnameTemplate, config.DefaultHostnameTemplate)
	})

	viper.Set(config.AutoRegister, "windows")
	err := run(Cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--autoRegister") {
		t.Fatalf("bad --autoRegister must fail startup, got %v", err)
	}

	viper.Set(config.AutoRegister, "flatcar")
	viper.Set(config.HostnameTemplate, "{{ .Nope }}")
	err = run(Cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--hostnameTemplate") {
		t.Fatalf("bad --hostnameTemplate must fail startup, got %v", err)
	}
}

func TestInitCommandWiring(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "viacli")
	var out bytes.Buffer
	Cmd.SetOut(&out)
	Cmd.SetArgs([]string{"init", dir})
	t.Cleanup(func() { Cmd.SetOut(nil); Cmd.SetArgs(nil) })
	if err := Cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config", "ignition.yaml")); err != nil {
		t.Fatalf("booty init <dir> must create the template: %v", err)
	}
	if !strings.Contains(out.String(), "Next steps") {
		t.Fatalf("output:\n%s", out.String())
	}
}
