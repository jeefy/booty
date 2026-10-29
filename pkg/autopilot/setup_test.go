package autopilot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/autopilot/report"
	"github.com/jeefy/booty/pkg/cni"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestFromConfigFallbacks(t *testing.T) {
	t.Cleanup(viper.Reset)
	viper.Set(config.Autopilot, config.AutopilotFull)
	viper.Set(config.AutopilotNamespace, "")
	viper.Set(config.AutopilotImage, "")
	viper.Set(config.AutopilotIssues, " /owner/repo/ ")
	viper.Set(config.GithubToken, " tok ")
	viper.Set(config.ControlPlane, "managed")
	viper.Set(config.AutopilotSoak, time.Hour)
	t.Setenv(config.PodNamespaceEnv, "booty-ns")
	t.Setenv(config.BootyImageEnv, "ghcr.io/jeefy/booty:test")

	s := FromConfig()
	if s.Mode != config.AutopilotFull || s.Namespace != "booty-ns" || s.Image != "ghcr.io/jeefy/booty:test" {
		t.Fatalf("env fallbacks: %+v", s)
	}
	if s.Issues != "owner/repo" || s.GitHubToken != "tok" {
		t.Fatalf("issues/token must be trimmed: %q %q", s.Issues, s.GitHubToken)
	}
	if !s.ManagedCNI || s.Soak != time.Hour {
		t.Fatalf("managed/soak: %+v", s)
	}

	t.Setenv(config.PodNamespaceEnv, "")
	viper.Set(config.AutopilotNamespace, "")
	if got := FromConfig().Namespace; got != config.DefaultAutopilotNamespace {
		t.Fatalf("namespace default: %q", got)
	}
	viper.Set(config.AutopilotNamespace, "explicit")
	viper.Set(config.AutopilotImage, "flag-image")
	if s := FromConfig(); s.Namespace != "explicit" || s.Image != "flag-image" {
		t.Fatalf("flags win over env: %+v", s)
	}
}

func TestValidateErrors(t *testing.T) {
	key := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(key, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		set  func(*Settings)
		want string // empty: valid
	}{
		{"bad mode", func(s *Settings) { s.Mode = "sometimes" }, "sometimes"},
		{"drain", func(s *Settings) { s.DrainTimeout = 0 }, config.AutopilotDrainTO},
		{"window", func(s *Settings) { s.HealthWindow = 0 }, config.AutopilotHealthWin},
		{"window off", func(s *Settings) { s.HealthWindow, s.Mode = 0, config.AutopilotOff }, ""},
		{"retry", func(s *Settings) { s.RetryAfter = 0 }, config.AutopilotRetryAfter},
		{"missing key", func(s *Settings) { s.SSHKey = key + ".missing" }, config.RebootSSHKey},
		{"key", func(s *Settings) { s.SSHKey = key }, ""},
		{"bad repo", func(s *Settings) { s.Issues, s.GitHubToken = "not a repo", "t" }, config.AutopilotIssues},
		{"no token", func(s *Settings) { s.Issues = "owner/repo" }, config.GithubToken},
		{"issues", func(s *Settings) { s.Issues, s.GitHubToken = "owner/repo", "t" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validSettings()
			tc.set(&s)
			err := s.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("want valid, got %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("want error naming %q, got %v", tc.want, err)
			}
		})
	}
}

func TestReportCNIAndPoster(t *testing.T) {
	if got := (Settings{CNI: "cilium"}).ReportCNI(); got != (report.CNI{}) {
		t.Fatalf("external CNI is not a fact: %+v", got)
	}
	if got := (Settings{ManagedCNI: true, CNI: cni.None}).ReportCNI(); got != (report.CNI{}) {
		t.Fatalf("none is omitted: %+v", got)
	}
	if got := (Settings{ManagedCNI: true, CNI: "cilium"}).ReportCNI(); got.Name != "cilium" || got.Version != cni.Pin("cilium") || got.Version == "" {
		t.Fatalf("pinned release: %+v", got)
	}
	if got := (Settings{ManagedCNI: true, CNI: "cilium", CNIRelease: "v9.9.9"}).ReportCNI(); got.Version != "v9.9.9" {
		t.Fatalf("--cniRelease wins: %+v", got)
	}
	if p := (Settings{}).Poster(); p != nil {
		t.Fatalf("no --autopilotIssues, no poster: %v", p)
	}
	if p := (Settings{Issues: "owner/repo", GitHubToken: "t"}).Poster(); p == nil {
		t.Fatal("--autopilotIssues builds a poster")
	}
}

func setupDataDir(t *testing.T) {
	t.Helper()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
}

func TestSetupOff(t *testing.T) {
	setupDataDir(t)
	s := validSettings()
	s.Mode = config.AutopilotOff
	a, err := Setup(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Controller != nil || a.Chooser == nil || a.Client != nil {
		t.Fatalf("off: no controller, a chooser, no client outside a cluster: %+v", a)
	}
	st := a.Status(context.Background())
	if st.Mode != config.AutopilotOff || !st.DryRun || st.Actuator != actuator.NameNone || st.Status != nil {
		t.Fatalf("off status: %+v", st)
	}
	if sum := a.Summary(); sum.Mode != config.AutopilotOff || sum.Actuator != actuator.NameNone || sum.Held == nil {
		t.Fatalf("off summary: %+v", sum)
	}
	a.LogStatus(context.Background())
}

func TestSetupGuardWithoutCluster(t *testing.T) {
	setupDataDir(t)
	a, err := Setup(validSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Controller == nil {
		t.Fatal("guard builds a controller")
	}
	st := a.Status(context.Background())
	if st.DryRun || st.Status == nil || st.HealthWindow != time.Minute.String() || st.RetryAfter != time.Minute.String() {
		t.Fatalf("guard status: %+v", st)
	}
	if st.Actuator != actuator.NameNone {
		t.Fatalf("no cluster, no key: actuator %q", st.Actuator)
	}
	sum := a.Summary()
	if sum.Mode != config.AutopilotGuard || sum.Held == nil {
		t.Fatalf("guard summary: %+v", sum)
	}
	// LogStatus must cope with every branch reachable without a cluster,
	// with and without issues configured.
	a.LogStatus(context.Background())
	a.Settings.Issues = "owner/repo"
	a.Settings.Soak, a.Settings.Cooldown = time.Hour, time.Hour
	a.LogStatus(context.Background())
}

func TestSetupKubeconfigError(t *testing.T) {
	setupDataDir(t)
	s := validSettings()
	s.Kubeconfig = filepath.Join(t.TempDir(), "missing")
	if _, err := Setup(s, nil); err == nil || !strings.Contains(err.Error(), config.Kubeconfig) {
		t.Fatalf("a bad --kubeconfig is a start-up error naming the flag, got %v", err)
	}
}

func TestSummaryNilAndOff(t *testing.T) {
	var a *Autopilot
	if s := a.Summary(); s.Mode != config.AutopilotOff || s.Held == nil {
		t.Fatalf("nil autopilot: %+v", s)
	}
	off := &Autopilot{Settings: Settings{Mode: config.AutopilotGuard}}
	if s := off.Summary(); s.Mode != config.AutopilotGuard || s.Actuator != actuator.NameNone {
		t.Fatalf("no controller reports the configured mode: %+v", s)
	}
}
