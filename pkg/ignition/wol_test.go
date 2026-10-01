package ignition

import "testing"

func TestWoLBuiltin(t *testing.T) {
	f, err := ParseFeatures("wol")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Fragment(Input{Hostname: "n1", Server: "s", MAC: "aa:bb:cc:dd:ee:01"}, f)
	if len(cfg.Storage.Files) != 1 || cfg.Storage.Files[0].Path != WoLLinkPath || *cfg.Storage.Files[0].Mode != 0o644 {
		t.Fatalf("wol only: %+v", cfg.Storage.Files)
	}
	want := "data:,%5BMatch%5D%0AMACAddress=aa:bb:cc:dd:ee:01%0A%0A%5BLink%5D%0AWakeOnLan=magic%0A"
	if got := *cfg.Storage.Files[0].Contents.Source; got != want {
		t.Fatalf("contents %q", got)
	}
	if cfg := Fragment(Input{Hostname: "n1", Server: "s"}, f); len(cfg.Storage.Files) != 0 {
		t.Fatal("no MAC, no .link file")
	}
	if WoLLink("") != "" {
		t.Fatal("empty MAC renders nothing")
	}
	if _, err := ParseFeatures("hostname,update,booted,sshkeys,health,wol"); err != nil {
		t.Fatalf("default list: %v", err)
	}
}
