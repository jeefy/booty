package hardware

import (
	"strings"
	"testing"
)

func TestValidateHostname(t *testing.T) {
	long := strings.Repeat("a", 63)
	tests := []struct {
		name string
		ok   bool
	}{
		{"node1", true},
		{"node-1", true},
		{"Node-1", true},
		{"node1.example.com", true},
		{"a", true},
		{"1", true},
		{long, true},
		{long + "." + long + "." + long + "." + strings.Repeat("a", 61), true},
		{"", false},
		{"-node", false},
		{"node-", false},
		{"node_1", false},
		{"Bad_Name!", false},
		{"node..1", false},
		{".node", false},
		{"node.", false},
		{"node 1", false},
		{long + "a", false},
		{long + "." + long + "." + long + "." + strings.Repeat("a", 62), false},
	}
	for _, tc := range tests {
		err := ValidateHostname(tc.name)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateHostname(%q) err=%v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestNewHostnameVars(t *testing.T) {
	v := NewHostnameVars("aa:bb:cc:dd:ee:ff", "10.0.0.5")
	if v.MAC != "aa:bb:cc:dd:ee:ff" || v.MACSuffix != "ddeeff" || v.MACFlat != "aabbccddeeff" || v.IP != "10.0.0.5" {
		t.Fatalf("unexpected vars %+v", v)
	}
}

func TestHostnameTemplate(t *testing.T) {
	const mac = "AA-BB-CC-DD-EE-42"
	tests := []struct {
		src     string
		want    string
		wantErr bool
	}{
		{"node-{{ .MACSuffix }}", "node-ddee42", false},
		{"{{ .MACFlat }}", "aabbccddee42", false},
		{"pxe-{{ .MACSuffix }}.lab.local", "pxe-ddee42.lab.local", false},
		{"static", "static", false},
		{"  node-{{ .MACSuffix }}\n", "node-ddee42", false},
		{"", "", true},
		{"   ", "", true},
		{"node-{{ .MACSuffix", "", true},
		{"node-{{ .Nope }}", "", true},
		{"{{ .MAC }}", "", true},
		{"-{{ .MACSuffix }}", "", true},
		{"{{ .MACSuffix }}_x", "", true},
	}
	for _, tc := range tests {
		tmpl, err := ParseHostnameTemplate(tc.src)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseHostnameTemplate(%q) should fail", tc.src)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseHostnameTemplate(%q): %v", tc.src, err)
			continue
		}
		got, err := tmpl.Render(mac, "10.0.0.5")
		if err != nil || got != tc.want {
			t.Errorf("Render(%q) = %q, %v; want %q", tc.src, got, err, tc.want)
		}
	}

	tmpl, err := ParseHostnameTemplate("{{ .IP }}-{{ .MACSuffix }}")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := tmpl.Render(mac, "10.0.0.5"); err != nil || got != "10.0.0.5-ddee42" {
		t.Fatalf("Render with IP = %q, %v", got, err)
	}
	if _, err := tmpl.Render("nope", "10.0.0.5"); err == nil {
		t.Fatal("Render must reject an invalid MAC")
	}
}

func TestValidateAutoRegisterOS(t *testing.T) {
	for _, ok := range []string{"", "flatcar", "coreos", "bluefin"} {
		if err := ValidateAutoRegisterOS(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"windows", "Flatcar", "none", "ublue"} {
		if err := ValidateAutoRegisterOS(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
