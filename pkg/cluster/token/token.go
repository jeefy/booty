// Package token generates kubeadm-style bootstrap tokens, persists them per
// purpose under --dataDir/cluster/tokens.json and encodes k0s join tokens
// (the gzip+base64 kubeconfig `k0s token pre-shared` produces) together
// with the bootstrap Secret the first k0s controller must apply.
package token

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jeefy/booty/pkg/config"
)

const (
	alphabet  = "abcdefghijklmnopqrstuvwxyz0123456789"
	idLen     = 6
	secretLen = 16

	// TTL is how long a persisted bootstrap token stays valid; RenewBefore
	// is how much remaining lifetime makes Current mint a fresh one.
	TTL         = 168 * time.Hour
	RenewBefore = 24 * time.Hour
)

// Pattern is the kubeadm bootstrap token format: <id>.<secret>.
var Pattern = regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`)

// Purpose names what a persisted token is for; each purpose has one
// current token.
type Purpose string

const (
	PurposeKubeadmWorker Purpose = "kubeadm-worker"
	PurposeK0sWorker     Purpose = "k0s-worker"
	PurposeK0sController Purpose = "k0s-controller"
)

// New returns a fresh bootstrap token from crypto/rand.
func New() (string, error) {
	id, err := randomString(idLen)
	if err != nil {
		return "", err
	}
	secret, err := randomString(secretLen)
	if err != nil {
		return "", err
	}
	return id + "." + secret, nil
}

// Split returns the id and secret halves of a bootstrap token.
func Split(token string) (id, secret string, err error) {
	if !Pattern.MatchString(token) {
		return "", "", fmt.Errorf("bootstrap token %q does not match %s", redact(token), Pattern)
	}
	id, secret, _ = strings.Cut(token, ".")
	return id, secret, nil
}

func randomString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

func redact(token string) string {
	if len(token) <= idLen {
		return token
	}
	return token[:idLen] + ".****"
}

// Entry is one persisted token with its expiry.
type Entry struct {
	Token   string    `json:"token"`
	Expires time.Time `json:"expires"`
}

// ID is the public half of the token.
func (e Entry) ID() string {
	id, _, _ := strings.Cut(e.Token, ".")
	return id
}

// Store persists one token per Purpose in a 0600 JSON file.
type Store struct {
	path    string
	mu      sync.Mutex
	now     func() time.Time
	entries map[Purpose]Entry
}

// Open loads the store at path, which need not exist yet.
func Open(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now, entries: map[Purpose]Entry{}}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.entries); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	for purpose, e := range s.entries {
		if !Pattern.MatchString(e.Token) {
			return nil, fmt.Errorf("%s: token for %s is malformed", path, purpose)
		}
	}
	return s, nil
}

// Path is the backing file.
func (s *Store) Path() string { return s.path }

// Current returns the token for purpose, minting and persisting a new one
// when none exists or fewer than RenewBefore of its TTL remain.
func (s *Store) Current(purpose Purpose) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if e, ok := s.entries[purpose]; ok && e.Expires.Sub(now) > RenewBefore {
		return e, nil
	}
	tok, err := New()
	if err != nil {
		return Entry{}, err
	}
	e := Entry{Token: tok, Expires: now.Add(TTL).UTC().Truncate(time.Second)}
	s.entries[purpose] = e
	if err := s.persist(); err != nil {
		delete(s.entries, purpose)
		return Entry{}, err
	}
	return e, nil
}

// Peek returns the stored token for purpose without minting.
func (s *Store) Peek(purpose Purpose) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[purpose]
	return e, ok
}

// Purposes lists the purposes with a stored token, sorted.
func (s *Store) Purposes() []Purpose {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Purpose, 0, len(s.entries))
	for p := range s.entries {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *Store) persist() error {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding tokens: %w", err)
	}
	if err := config.WriteFileAtomic(s.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", s.path, err)
	}
	return nil
}
