package token

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestNewMatchesKubeadmPattern(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		tok, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if !Pattern.MatchString(tok) {
			t.Fatalf("token %q does not match %s", tok, Pattern)
		}
		if seen[tok] {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = true
		id, secret, err := Split(tok)
		if err != nil || len(id) != 6 || len(secret) != 16 {
			t.Fatalf("Split(%q) = %q %q %v", tok, id, secret, err)
		}
	}
	if _, _, err := Split("ABCDEF.0123456789abcdef"); err == nil {
		t.Fatal("uppercase must be rejected")
	}
	if _, _, err := Split("abcdef.0123456789abcde"); err == nil {
		t.Fatal("short secret must be rejected")
	}
}

func TestStorePersistsAndRenews(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster", "tokens.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	first, err := s.Current(PurposeKubeadmWorker)
	if err != nil {
		t.Fatal(err)
	}
	if !Pattern.MatchString(first.Token) || !first.Expires.Equal(now.Add(TTL)) {
		t.Fatalf("first entry %+v", first)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("tokens.json mode %v err=%v, want 0600", info.Mode(), err)
	}
	if again, _ := s.Current(PurposeKubeadmWorker); again.Token != first.Token {
		t.Fatal("a fresh token must be reused")
	}
	if _, ok := s.Peek(PurposeK0sWorker); ok {
		t.Fatal("Peek must not mint")
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = s.now
	if got, _ := reopened.Current(PurposeKubeadmWorker); got.Token != first.Token || !got.Expires.Equal(first.Expires) {
		t.Fatalf("reopened store must return the persisted token: %+v vs %+v", got, first)
	}

	now = now.Add(TTL - RenewBefore + time.Minute)
	renewed, err := s.Current(PurposeKubeadmWorker)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Token == first.Token {
		t.Fatal("a token with under 24h left must be replaced")
	}
	if !renewed.Expires.Equal(now.Add(TTL)) {
		t.Fatalf("renewed expiry %v", renewed.Expires)
	}
	other, err := s.Current(PurposeK0sController)
	if err != nil || other.Token == renewed.Token {
		t.Fatalf("purposes must not share tokens: %v %+v", err, other)
	}
	if got := s.Purposes(); len(got) != 2 || got[0] != PurposeK0sController || got[1] != PurposeKubeadmWorker {
		t.Fatalf("Purposes %v", got)
	}

	if err := os.WriteFile(path, []byte(`{"kubeadm-worker":{"token":"bad","expires":"2026-01-01T00:00:00Z"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed persisted token must fail to load, got %v", err)
	}
}

func TestEncodeK0sRoundTrip(t *testing.T) {
	ca := []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n")
	tok := "abcdef.0123456789abcdef"

	worker, err := EncodeK0s(K0sWorker, "10.0.0.1", ca, tok)
	if err != nil {
		t.Fatal(err)
	}
	j, err := ParseK0s(worker)
	if err != nil {
		t.Fatal(err)
	}
	if j.Role != K0sWorker || j.Server != "https://10.0.0.1:6443" || j.User != "kubelet-bootstrap" || j.Token != tok || !bytes.Equal(j.CACert, ca) {
		t.Fatalf("worker join %+v", j)
	}

	controller, err := EncodeK0s(K0sController, "cp.example.org", ca, tok)
	if err != nil {
		t.Fatal(err)
	}
	if j, err = ParseK0s(controller); err != nil || j.Role != K0sController || j.Server != "https://cp.example.org:9443" || j.User != "controller-bootstrap" {
		t.Fatalf("controller join %+v err=%v", j, err)
	}

	custom, err := EncodeK0s(K0sWorker, "10.0.0.1:8443", ca, tok)
	if err != nil {
		t.Fatal(err)
	}
	if j, _ = ParseK0s(custom); j.Server != "https://10.0.0.1:8443" {
		t.Fatalf("explicit port must win: %s", j.Server)
	}

	for _, bad := range []struct {
		role K0sRole
		ep   string
		ca   []byte
		tok  string
	}{
		{"master", "10.0.0.1", ca, tok},
		{K0sWorker, "", ca, tok},
		{K0sWorker, "https://10.0.0.1", ca, tok},
		{K0sWorker, "10.0.0.1", nil, tok},
		{K0sWorker, "10.0.0.1", ca, "nope"},
	} {
		if _, err := EncodeK0s(bad.role, bad.ep, bad.ca, bad.tok); err == nil {
			t.Errorf("EncodeK0s(%q,%q,%d bytes,%q) must fail", bad.role, bad.ep, len(bad.ca), bad.tok)
		}
	}
	if _, err := DecodeK0s("not base64!"); err == nil {
		t.Fatal("invalid base64 must fail")
	}
	if _, err := DecodeK0s(base64.StdEncoding.EncodeToString([]byte("not gzip"))); err == nil {
		t.Fatal("invalid gzip must fail")
	}
}

// The fixtures are the real output of
//
//	podman run --rm docker.io/k0sproject/k0s:v1.36.4-k0s.1 k0s token pre-shared \
//	  --role worker|controller --cert ca.crt --url https://10.0.0.1:6443|9443 --valid 168h
//
// captured 2026-09-26 with a throwaway CA (testdata/k0s-ca.crt).
func TestK0sFixturesDecodeWithOurDecoder(t *testing.T) {
	ca, err := os.ReadFile("testdata/k0s-ca.crt")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		file   string
		role   K0sRole
		server string
		user   string
	}{
		{"k0s-worker.token", K0sWorker, "https://10.0.0.1:6443", "kubelet-bootstrap"},
		{"k0s-controller.token", K0sController, "https://10.0.0.1:9443", "controller-bootstrap"},
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		j, err := ParseK0s(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if j.Role != tc.role || j.Server != tc.server || j.User != tc.user || !Pattern.MatchString(j.Token) || !bytes.Equal(j.CACert, ca) {
			t.Fatalf("%s: %+v", tc.file, j)
		}

		ours, err := EncodeK0s(tc.role, "10.0.0.1", ca, j.Token)
		if err != nil {
			t.Fatal(err)
		}
		theirs, err := DecodeK0s(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		mine, err := DecodeK0s(ours)
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		if err := yaml.Unmarshal(theirs, &a); err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(mine, &b); err != nil {
			t.Fatal(err)
		}
		if !equalYAML(a, b) {
			t.Fatalf("%s: our kubeconfig differs from k0s's\nk0s:\n%s\nours:\n%s", tc.file, theirs, mine)
		}
	}
}

func TestK0sBootstrapSecretMatchesFixtures(t *testing.T) {
	for _, tc := range []struct {
		token  string
		secret string
		role   K0sRole
	}{
		{"k0s-worker.token", "k0s-worker-secret.yaml", K0sWorker},
		{"k0s-controller.token", "k0s-controller-secret.yaml", K0sController},
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", tc.token))
		if err != nil {
			t.Fatal(err)
		}
		j, err := ParseK0s(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join("testdata", tc.secret))
		if err != nil {
			t.Fatal(err)
		}
		var theirs map[string]any
		if err := yaml.Unmarshal(want, &theirs); err != nil {
			t.Fatal(err)
		}
		expStr, err := base64.StdEncoding.DecodeString(theirs["data"].(map[string]any)["expiration"].(string))
		if err != nil {
			t.Fatal(err)
		}
		expires, err := time.Parse(time.RFC3339, string(expStr))
		if err != nil {
			t.Fatal(err)
		}
		got, err := K0sBootstrapSecret(tc.role, j.Token, expires)
		if err != nil {
			t.Fatal(err)
		}
		var ours map[string]any
		if err := yaml.Unmarshal(got, &ours); err != nil {
			t.Fatal(err)
		}
		if !equalYAML(theirs, ours) {
			t.Fatalf("%s: Secret differs from k0s's\nk0s:\n%s\nours:\n%s", tc.secret, want, got)
		}
	}
	if _, err := K0sBootstrapSecret("master", "abcdef.0123456789abcdef", time.Now()); err == nil {
		t.Fatal("unknown role must fail")
	}
}

func equalYAML(a, b any) bool {
	x, errA := yaml.Marshal(a)
	y, errB := yaml.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}
