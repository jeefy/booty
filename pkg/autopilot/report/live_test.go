//go:build live

package report

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLivePostAgainstScratchRepo files a redacted fixture report three
// times against the real GitHub API: two machines (different DMI hashes)
// and the first one again. Expected: one issue, one comment, one no-op.
// It needs GITHUB_TOKEN (or `gh auth token`) and LIVE_REPO (default
// jeefy/booty-autopilot-scratch); run with `go test -tags live -run
// TestLivePost ./pkg/autopilot/report/ -v`. The issue number is printed
// so the caller can `gh issue view` and close it.
func TestLivePostAgainstScratchRepo(t *testing.T) {
	repo := os.Getenv("LIVE_REPO")
	if repo == "" {
		repo = "jeefy/booty-autopilot-scratch"
	}
	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" {
		out, err := exec.Command("gh", "auth", "token").Output()
		if err != nil {
			t.Skipf("no GITHUB_TOKEN and gh auth token failed: %v", err)
		}
		token = strings.TrimSpace(string(out))
	}
	p := NewPoster(repo, token, "[autopilot test] ")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	version := fmt.Sprintf("26.09.%d", time.Now().Unix()%100000)
	first := fixture()
	first.Version = version
	second := fixture()
	second.Version = version
	second.DMIHash = "0123456789abcdef"
	second.Hardware.Product = "ProDesk 400 G4"

	res1, err := p.Post(ctx, first, Build(first))
	if err != nil {
		t.Fatalf("first post: %v", err)
	}
	t.Logf("first: %+v", res1)
	if res1.Action != ActionCreated {
		t.Fatalf("first post must create: %+v", res1)
	}

	// Let the search index catch up; the listing fallback covers a
	// stale index, but the intent here is to exercise the search path.
	time.Sleep(8 * time.Second)

	res2, err := p.Post(ctx, second, Build(second))
	if err != nil {
		t.Fatalf("second post: %v", err)
	}
	t.Logf("second: %+v", res2)
	if res2.Action != ActionCommented || res2.Number != res1.Number {
		t.Fatalf("second machine must comment on #%d: %+v", res1.Number, res2)
	}

	res3, err := p.Post(ctx, first, Build(first))
	if err != nil {
		t.Fatalf("third post: %v", err)
	}
	t.Logf("third: %+v", res3)
	if res3.Action != ActionAlreadyReported || res3.Number != res1.Number {
		t.Fatalf("same machine again must be a no-op: %+v", res3)
	}
	fmt.Printf("LIVE_ISSUE=%d LIVE_URL=%s LIVE_VERSION=%s\n", res1.Number, res1.URL, version)
}
