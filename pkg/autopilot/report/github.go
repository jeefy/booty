package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultGitHubAPI is where --autopilotIssues posts unless a test says
// otherwise.
const DefaultGitHubAPI = "https://api.github.com"

// IssueLabel is put on every issue the poster creates.
const IssueLabel = "autopilot"

// Actions a Post can end with.
const (
	ActionCreated         = "created"
	ActionCommented       = "commented"
	ActionAlreadyReported = "already-reported"
)

// HTTPError is a GitHub answer the poster could not use; Status is what
// the controller puts in its report-post-failed event.
type HTTPError struct {
	Status int
	URL    string
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.URL, e.Status, strings.TrimSpace(e.Body))
}

// RateLimited reports whether the answer was GitHub's rate limit.
func (e *HTTPError) RateLimited() bool {
	return e.Status == http.StatusTooManyRequests || (e.Status == http.StatusForbidden && strings.Contains(strings.ToLower(e.Body), "rate limit"))
}

// Poster files reports as GitHub issues in Repo (owner/name) with Token
// (issues:write). One issue per release: the first machine creates it,
// every further machine (a different DMI hash) comments, the same
// machine again does nothing. TitlePrefix goes in front of the issue
// title and the comment's first line (the dedupe marker is unchanged), so
// a scratch repository's issues say where they came from. Every call
// retries Tries times with exponential backoff on network errors, 5xx and
// rate limits.
type Poster struct {
	Repo        string
	Token       string
	TitlePrefix string
	BaseURL     string
	Client      *http.Client
	Tries       int
	Backoff     time.Duration
	Sleep       func(context.Context, time.Duration) error
}

// NewPoster is a Poster against api.github.com with 3 tries from 1 s.
func NewPoster(repo, token, titlePrefix string) *Poster {
	return &Poster{Repo: repo, Token: token, TitlePrefix: titlePrefix}
}

// ValidateRepo checks an owner/repo value.
func ValidateRepo(repo string) error {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(repo, " \t\n?#") {
		return fmt.Errorf("%q is not owner/repo", repo)
	}
	return nil
}

func (p *Poster) defaults() {
	if p.BaseURL == "" {
		p.BaseURL = DefaultGitHubAPI
	}
	if p.Client == nil {
		p.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if p.Tries <= 0 {
		p.Tries = 3
	}
	if p.Backoff <= 0 {
		p.Backoff = time.Second
	}
	if p.Sleep == nil {
		p.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
}

// Result is what a Post did.
type Result struct {
	Action string `json:"action"`
	URL    string `json:"url"`
	Number int    `json:"number"`
}

type issue struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	State   string `json:"state"`
}

// Post files rep (built from in) to the repository. It never touches the
// controller's state; the caller records the Result.
func (p *Poster) Post(ctx context.Context, in Input, rep Report) (Result, error) {
	p.defaults()
	if err := ValidateRepo(p.Repo); err != nil {
		return Result{}, err
	}
	doc := rep.Document()
	if doc == nil {
		return Result{}, errors.New("report has no JSON document")
	}
	existing, err := p.findIssue(ctx, in.OS, in.Version)
	if err != nil {
		return Result{}, err
	}
	marker := Marker(in.OS, in.Version, in.DMIHash)
	if existing == nil {
		p.ensureLabel(ctx)
		created, err := p.createIssue(ctx, in, doc, rep.Markdown)
		if err != nil {
			return Result{}, err
		}
		return Result{Action: ActionCreated, URL: created.HTMLURL, Number: created.Number}, nil
	}
	reported, err := p.alreadyReported(ctx, existing, marker)
	if err != nil {
		return Result{}, err
	}
	if reported {
		return Result{Action: ActionAlreadyReported, URL: existing.HTMLURL, Number: existing.Number}, nil
	}
	if err := p.comment(ctx, existing.Number, commentBody(p.TitlePrefix, in, doc, marker)); err != nil {
		return Result{}, err
	}
	return Result{Action: ActionCommented, URL: existing.HTMLURL, Number: existing.Number}, nil
}

// findIssue looks for the release's issue, open or closed, first through
// the search API and, when that is rate-limited or refused, by listing
// the labelled issues and reading their bodies.
func (p *Poster) findIssue(ctx context.Context, osName, version string) (*issue, error) {
	prefix := MarkerPrefix(osName, version)
	q := url.Values{"q": {fmt.Sprintf("repo:%s is:issue %q in:body", p.Repo, prefix)}, "per_page": {"20"}}
	var search struct {
		Items []issue `json:"items"`
	}
	err := p.get(ctx, "/search/issues?"+q.Encode(), &search, false)
	if err == nil {
		for i := range search.Items {
			if strings.Contains(search.Items[i].Body, prefix) {
				return &search.Items[i], nil
			}
		}
		return nil, nil
	}
	var he *HTTPError
	if !errors.As(err, &he) {
		return nil, err
	}
	fallback := he.RateLimited() || he.Status == http.StatusForbidden || he.Status == http.StatusUnprocessableEntity
	if !fallback {
		return nil, err
	}
	slog.Warn("GitHub issue search unavailable; listing labelled issues instead", "repo", p.Repo, "status", he.Status)
	for page := 1; page <= 5; page++ {
		var items []issue
		path := fmt.Sprintf("/repos/%s/issues?state=all&labels=%s&per_page=100&page=%d", p.Repo, IssueLabel, page)
		if err := p.get(ctx, path, &items, true); err != nil {
			return nil, err
		}
		for i := range items {
			if strings.Contains(items[i].Body, prefix) {
				return &items[i], nil
			}
		}
		if len(items) < 100 {
			break
		}
	}
	return nil, nil
}

// alreadyReported is true when the issue body or one of its comments
// carries this machine's marker.
func (p *Poster) alreadyReported(ctx context.Context, is *issue, marker string) (bool, error) {
	if strings.Contains(is.Body, marker) {
		return true, nil
	}
	for page := 1; page <= 10; page++ {
		var comments []struct {
			Body string `json:"body"`
		}
		path := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100&page=%d", p.Repo, is.Number, page)
		if err := p.get(ctx, path, &comments, true); err != nil {
			return false, err
		}
		for _, c := range comments {
			if strings.Contains(c.Body, marker) {
				return true, nil
			}
		}
		if len(comments) < 100 {
			break
		}
	}
	return false, nil
}

func (p *Poster) ensureLabel(ctx context.Context) {
	var got struct {
		Name string `json:"name"`
	}
	err := p.get(ctx, fmt.Sprintf("/repos/%s/labels/%s", p.Repo, IssueLabel), &got, true)
	if err == nil {
		return
	}
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusNotFound {
		slog.Warn("Could not check the autopilot label; creating the issue without it if need be", "repo", p.Repo, "error", err)
		return
	}
	body := map[string]string{"name": IssueLabel, "color": "5319e7", "description": "Filed by Booty's autopilot"}
	if err := p.post(ctx, fmt.Sprintf("/repos/%s/labels", p.Repo), body, nil); err != nil {
		slog.Warn("Could not create the autopilot label; the issue is filed without it", "repo", p.Repo, "error", err)
	}
}

func (p *Poster) createIssue(ctx context.Context, in Input, doc *Document, markdown string) (*issue, error) {
	body := map[string]any{"title": IssueTitle(p.TitlePrefix, in, doc), "body": markdown, "labels": []string{IssueLabel}}
	var created issue
	if err := p.post(ctx, fmt.Sprintf("/repos/%s/issues", p.Repo), body, &created); err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.Status == http.StatusUnprocessableEntity {
			delete(body, "labels")
			if err2 := p.post(ctx, fmt.Sprintf("/repos/%s/issues", p.Repo), body, &created); err2 == nil {
				return &created, nil
			}
		}
		return nil, err
	}
	return &created, nil
}

func (p *Poster) comment(ctx context.Context, number int, text string) error {
	return p.post(ctx, fmt.Sprintf("/repos/%s/issues/%d/comments", p.Repo, number), map[string]string{"body": text}, nil)
}

// IssueTitle is "<prefix>Bluefin Server <version>: <class> on <vendor>
// <product> (autopilot)"; prefix is --autopilotIssueTitlePrefix verbatim,
// usually empty.
func IssueTitle(prefix string, in Input, doc *Document) string {
	hw := strings.TrimSpace(doc.Hardware.Vendor + " " + doc.Hardware.Product)
	if hw == "" {
		hw = "unknown hardware"
	}
	return fmt.Sprintf("%s%s %s: %s on %s (autopilot)", prefix, osTitle(in.OS), in.Version, orUnknown(doc.Class), hw)
}

func commentBody(prefix string, in Input, doc *Document, marker string) string {
	var b strings.Builder
	b.WriteString(marker)
	b.WriteString("\n\n")
	hw := strings.TrimSpace(doc.Hardware.Vendor + " " + doc.Hardware.Product)
	if hw == "" {
		hw = "unknown hardware"
	}
	fmt.Fprintf(&b, "%sAnother machine (dmi-hash `%s`, %s, BIOS %s, boot path `%s`) hit this on `%s`: **%s**, %d attempt(s); %s.\n\n",
		prefix, orUnknown(doc.DMIHash), hw, orDash(doc.Hardware.BIOSVersion), doc.Hardware.BootPath, in.Version, orUnknown(doc.Class), doc.AttemptCount, orDash(doc.RollbackResult))
	if len(doc.FailedUnits) > 0 {
		b.WriteString("Failed units: `" + strings.Join(doc.FailedUnits, "`, `") + "`\n\n")
	}
	if len(doc.JournalErrors) > 0 {
		b.WriteString("<details><summary>Journal errors (redacted excerpt)</summary>\n\n```\n")
		for _, l := range doc.JournalErrors {
			b.WriteString(l)
			b.WriteString("\n")
		}
		b.WriteString("```\n\n</details>\n")
	}
	return b.String()
}

func (p *Poster) get(ctx context.Context, path string, out any, retryRateLimit bool) error {
	return p.do(ctx, http.MethodGet, path, nil, out, retryRateLimit)
}

func (p *Poster) post(ctx context.Context, path string, body, out any) error {
	return p.do(ctx, http.MethodPost, path, body, out, true)
}

// do issues one API call with retries: network errors and 5xx always,
// rate limits when retryRateLimit (the search call falls back instead).
func (p *Poster) do(ctx context.Context, method, path string, body, out any, retryRateLimit bool) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	var last error
	for attempt := 0; attempt < p.Tries; attempt++ {
		if attempt > 0 {
			if err := p.Sleep(ctx, p.Backoff<<uint(attempt-1)); err != nil {
				return err
			}
		}
		err := p.once(ctx, method, path, payload, out)
		if err == nil {
			return nil
		}
		last = err
		var he *HTTPError
		if errors.As(err, &he) {
			switch {
			case he.Status >= 500, he.RateLimited() && retryRateLimit:
				continue
			}
			return err
		}
		if ctx.Err() != nil {
			return err
		}
	}
	return last
}

func (p *Poster) once(ctx context.Context, method, path string, payload []byte, out any) error {
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "booty-autopilot")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{Status: resp.StatusCode, URL: method + " " + path, Body: clipBody(data)}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s %s: decoding: %w", method, path, err)
		}
	}
	return nil
}

func clipBody(b []byte) string {
	const n = 300
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}

// StatusOf is the HTTP status behind err, or 0 for a network error.
func StatusOf(err error) int {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// StatusText is what the report-post-failed event carries.
func StatusText(err error) string {
	if s := StatusOf(err); s != 0 {
		return strconv.Itoa(s)
	}
	return "network"
}
