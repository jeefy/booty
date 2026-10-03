package report

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub is enough of api.github.com for the poster: issue search
// (optionally rate-limited), issue listing by label, labels, issue and
// comment creation.
type fakeGitHub struct {
	mu          sync.Mutex
	t           *testing.T
	repo        string
	token       string
	issues      []*ghIssue
	labels      map[string]bool
	searchFails int
	failCreate  int
	calls       []string
	seen        []*http.Request
}

type ghIssue struct {
	Number   int      `json:"number"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	HTMLURL  string   `json:"html_url"`
	State    string   `json:"state"`
	Labels   []string `json:"-"`
	Comments []string `json:"-"`
}

func newFakeGitHub(t *testing.T) (*fakeGitHub, *httptest.Server) {
	f := &fakeGitHub{t: t, repo: "jeefy/scratch", token: "tok", labels: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.seen = append(f.seen, r)
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	repoPrefix := "/repos/" + f.repo
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
		if f.searchFails > 0 {
			f.searchFails--
			w.Header().Set("X-RateLimit-Remaining", "0")
			http.Error(w, `{"message":"API rate limit exceeded for user"}`, http.StatusForbidden)
			return
		}
		q := r.URL.Query().Get("q")
		needle := q[strings.Index(q, `"`)+1 : strings.LastIndex(q, `"`)]
		var items []*ghIssue
		for _, is := range f.issues {
			if strings.Contains(is.Body, needle) {
				items = append(items, is)
			}
		}
		writeJSON(w, map[string]any{"total_count": len(items), "items": items})
	case r.Method == http.MethodGet && r.URL.Path == repoPrefix+"/issues":
		var items []*ghIssue
		label := r.URL.Query().Get("labels")
		for _, is := range f.issues {
			if label == "" || contains(is.Labels, label) {
				items = append(items, is)
			}
		}
		if items == nil {
			items = []*ghIssue{}
		}
		writeJSON(w, items)
	case r.Method == http.MethodPost && r.URL.Path == repoPrefix+"/issues":
		if f.failCreate > 0 {
			f.failCreate--
			http.Error(w, `{"message":"boom"}`, http.StatusBadGateway)
			return
		}
		var in struct {
			Title  string   `json:"title"`
			Body   string   `json:"body"`
			Labels []string `json:"labels"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		is := &ghIssue{Number: len(f.issues) + 1, Title: in.Title, Body: in.Body, State: "open", Labels: in.Labels}
		is.HTMLURL = fmt.Sprintf("https://github.com/%s/issues/%d", f.repo, is.Number)
		f.issues = append(f.issues, is)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, is)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, repoPrefix+"/labels/"):
		name := strings.TrimPrefix(r.URL.Path, repoPrefix+"/labels/")
		if !f.labels[name] {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]string{"name": name})
	case r.Method == http.MethodPost && r.URL.Path == repoPrefix+"/labels":
		var in struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(body, &in)
		f.labels[in.Name] = true
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, in)
	case strings.HasPrefix(r.URL.Path, repoPrefix+"/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
		n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, repoPrefix+"/issues/"), "/comments"))
		if n < 1 || n > len(f.issues) {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		is := f.issues[n-1]
		if r.Method == http.MethodGet {
			out := []map[string]string{}
			for _, c := range is.Comments {
				out = append(out, map[string]string{"body": c})
			}
			writeJSON(w, out)
			return
		}
		var in struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(body, &in)
		is.Comments = append(is.Comments, in.Body)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": len(is.Comments), "body": in.Body})
	default:
		http.Error(w, `{"message":"Not Found: `+r.Method+" "+r.URL.Path+`"}`, http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (f *fakeGitHub) countCalls(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func newTestPoster(srv *httptest.Server, f *fakeGitHub) (*Poster, *[]time.Duration) {
	var slept []time.Duration
	p := &Poster{Repo: f.repo, Token: f.token, BaseURL: srv.URL, Client: srv.Client(), Sleep: func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}}
	return p, &slept
}

func TestPostCreatesThenCommentsThenNoops(t *testing.T) {
	f, srv := newFakeGitHub(t)
	p, _ := newTestPoster(srv, f)
	ctx := context.Background()

	first := fixture()
	res, err := p.Post(ctx, first, Build(first))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionCreated || res.Number != 1 || res.URL != "https://github.com/jeefy/scratch/issues/1" {
		t.Fatalf("first post: %+v", res)
	}
	f.mu.Lock()
	is := f.issues[0]
	f.mu.Unlock()
	if is.Title != "Bluefin Server 26.09.700: failed-units on HP HP EliteDesk 800 G1 DM (autopilot)" {
		t.Fatalf("title %q", is.Title)
	}
	if !contains(is.Labels, IssueLabel) || !f.labels[IssueLabel] {
		t.Fatalf("label must be created and applied: %v %v", is.Labels, f.labels)
	}
	if !strings.Contains(is.Body, Marker("bluefin", "26.09.700", "9f86d081884c7d65")) || piiPattern.MatchString(is.Body) {
		t.Fatalf("issue body: %s", is.Body)
	}

	second := fixture()
	second.DMIHash = "0123456789abcdef"
	second.Hardware.Product = "ProDesk 400 G4"
	res, err = p.Post(ctx, second, Build(second))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionCommented || res.Number != 1 {
		t.Fatalf("second machine: %+v", res)
	}
	f.mu.Lock()
	comments := append([]string(nil), is.Comments...)
	f.mu.Unlock()
	if len(comments) != 1 || !strings.Contains(comments[0], Marker("bluefin", "26.09.700", "0123456789abcdef")) || !strings.Contains(comments[0], "Another machine (dmi-hash `0123456789abcdef`, HP ProDesk 400 G4") || !strings.Contains(comments[0], "failed-units") || piiPattern.MatchString(comments[0]) {
		t.Fatalf("comment: %q", comments)
	}
	if f.countCalls("POST /repos/jeefy/scratch/issues") != 2 {
		t.Fatalf("calls: %v", f.calls)
	}

	for _, again := range []Input{first, second} {
		res, err = p.Post(ctx, again, Build(again))
		if err != nil {
			t.Fatal(err)
		}
		if res.Action != ActionAlreadyReported || res.Number != 1 {
			t.Fatalf("repeat from the same machine: %+v", res)
		}
	}
	f.mu.Lock()
	n, nc := len(f.issues), len(is.Comments)
	f.mu.Unlock()
	if n != 1 || nc != 1 {
		t.Fatalf("repeats must not post: %d issues, %d comments", n, nc)
	}

	other := fixture()
	other.Version = "26.09.701"
	res, err = p.Post(ctx, other, Build(other))
	if err != nil || res.Action != ActionCreated || res.Number != 2 {
		t.Fatalf("another release gets its own issue: %+v %v", res, err)
	}
	if f.countCalls("POST /repos/jeefy/scratch/labels") != 1 {
		t.Fatalf("label created once: %v", f.calls)
	}
}

// TestPostWithTitlePrefix: --autopilotIssueTitlePrefix goes in front of
// the issue title and the comment's first line, verbatim, while the body,
// the marker and therefore the dedupe stay exactly as without it.
func TestPostWithTitlePrefix(t *testing.T) {
	in := fixture()
	doc := Build(in).Document()
	if got := IssueTitle("", in, doc); got != "Bluefin Server 26.09.700: failed-units on HP HP EliteDesk 800 G1 DM (autopilot)" {
		t.Fatalf("title without prefix %q", got)
	}
	if got := IssueTitle("[autopilot test] ", in, doc); got != "[autopilot test] Bluefin Server 26.09.700: failed-units on HP HP EliteDesk 800 G1 DM (autopilot)" {
		t.Fatalf("title with prefix %q", got)
	}

	f, srv := newFakeGitHub(t)
	p, _ := newTestPoster(srv, f)
	p.TitlePrefix = "[autopilot test] "
	ctx := context.Background()
	res, err := p.Post(ctx, in, Build(in))
	if err != nil || res.Action != ActionCreated {
		t.Fatalf("%+v %v", res, err)
	}
	f.mu.Lock()
	is := f.issues[0]
	f.mu.Unlock()
	if is.Title != "[autopilot test] Bluefin Server 26.09.700: failed-units on HP HP EliteDesk 800 G1 DM (autopilot)" {
		t.Fatalf("title %q", is.Title)
	}
	if strings.Contains(is.Body, "[autopilot test]") || !strings.Contains(is.Body, Marker("bluefin", "26.09.700", "9f86d081884c7d65")) {
		t.Fatalf("the prefix must not touch the body or its marker: %s", is.Body)
	}

	second := fixture()
	second.DMIHash = "0123456789abcdef"
	res, err = p.Post(ctx, second, Build(second))
	if err != nil || res.Action != ActionCommented || res.Number != 1 {
		t.Fatalf("second machine must find the prefixed issue by its marker: %+v %v", res, err)
	}
	f.mu.Lock()
	comments := append([]string(nil), is.Comments...)
	f.mu.Unlock()
	if len(comments) != 1 || !strings.HasPrefix(comments[0], Marker("bluefin", "26.09.700", "0123456789abcdef")+"\n\n[autopilot test] Another machine (dmi-hash `0123456789abcdef`") {
		t.Fatalf("comment: %q", comments)
	}

	unprefixed, _ := newTestPoster(srv, f)
	res, err = unprefixed.Post(ctx, in, Build(in))
	if err != nil || res.Action != ActionAlreadyReported || res.Number != 1 {
		t.Fatalf("a poster without the prefix must still recognise the issue: %+v %v", res, err)
	}
}

func TestPostFallsBackToListingWhenSearchIsRateLimited(t *testing.T) {
	f, srv := newFakeGitHub(t)
	p, slept := newTestPoster(srv, f)
	ctx := context.Background()
	in := fixture()
	if _, err := p.Post(ctx, in, Build(in)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.searchFails = 10
	f.mu.Unlock()
	other := fixture()
	other.DMIHash = "feedfacefeedface"
	res, err := p.Post(ctx, other, Build(other))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionCommented {
		t.Fatalf("listing fallback must find the issue: %+v", res)
	}
	if f.countCalls("GET /repos/jeefy/scratch/issues") == 0 {
		t.Fatalf("expected a listing call: %v", f.calls)
	}
	if len(*slept) != 0 {
		t.Fatalf("search rate limit must fall back, not sleep: %v", *slept)
	}
}

func TestPostRetriesServerErrorsWithBackoff(t *testing.T) {
	f, srv := newFakeGitHub(t)
	p, slept := newTestPoster(srv, f)
	f.failCreate = 2
	in := fixture()
	res, err := p.Post(context.Background(), in, Build(in))
	if err != nil || res.Action != ActionCreated {
		t.Fatalf("%+v %v", res, err)
	}
	if len(*slept) != 2 || (*slept)[0] != time.Second || (*slept)[1] != 2*time.Second {
		t.Fatalf("backoff: %v", *slept)
	}

	f.mu.Lock()
	f.failCreate = 5
	f.issues = nil
	f.mu.Unlock()
	*slept = nil
	_, err = p.Post(context.Background(), in, Build(in))
	if err == nil || StatusOf(err) != http.StatusBadGateway || StatusText(err) != "502" {
		t.Fatalf("after 3 tries: %v", err)
	}
	if len(*slept) != 2 {
		t.Fatalf("exactly three tries: %v", *slept)
	}
}

func TestPostReportsForbiddenWithoutPanicking(t *testing.T) {
	f, srv := newFakeGitHub(t)
	p, _ := newTestPoster(srv, f)
	p.Token = "wrong"
	in := fixture()
	_, err := p.Post(context.Background(), in, Build(in))
	if err == nil || StatusOf(err) != http.StatusUnauthorized {
		t.Fatalf("bad token: %v", err)
	}

	p.Token = f.token
	p.Repo = "nope"
	if _, err := p.Post(context.Background(), in, Build(in)); err == nil {
		t.Fatal("invalid repo must fail before any call")
	}
	if err := ValidateRepo("owner/repo"); err != nil {
		t.Fatal(err)
	}

	dead := &Poster{Repo: f.repo, Token: f.token, BaseURL: "http://127.0.0.1:1", Client: &http.Client{Timeout: time.Second}, Sleep: func(context.Context, time.Duration) error { return nil }}
	_, err = dead.Post(context.Background(), in, Build(in))
	if err == nil || StatusOf(err) != 0 || StatusText(err) != "network" {
		t.Fatalf("network error: %v", err)
	}
}
