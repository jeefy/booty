package versions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeRegistry is the slice of the OCI distribution API oras-go needs to
// push and pull an artifact: monolithic blob uploads, manifests by tag or
// digest, and the tag list. It stands in for a real registry in the
// Bluefin OCI tests.
type fakeRegistry struct {
	mu        sync.Mutex
	blobs     map[string][]byte
	manifests map[string]manifestEntry
	tags      map[string]map[string]string
	uploads   int
}

type manifestEntry struct {
	mediaType string
	body      []byte
}

func newFakeRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	r := &fakeRegistry{blobs: map[string][]byte{}, manifests: map[string]manifestEntry{}, tags: map[string]map[string]string{}}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func sha256Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r *fakeRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := strings.TrimPrefix(req.URL.Path, "/v2/")
	if path == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	switch {
	case strings.Contains(path, "/blobs/uploads/"):
		r.upload(w, req, path)
	case strings.Contains(path, "/blobs/"):
		r.blob(w, req, path)
	case strings.Contains(path, "/manifests/"):
		r.manifest(w, req, path)
	case strings.HasSuffix(path, "/tags/list"):
		r.tagList(w, strings.TrimSuffix(path, "/tags/list"))
	default:
		http.NotFound(w, req)
	}
}

func (r *fakeRegistry) upload(w http.ResponseWriter, req *http.Request, path string) {
	repo := path[:strings.Index(path, "/blobs/uploads/")]
	switch req.Method {
	case http.MethodPost:
		r.mu.Lock()
		r.uploads++
		id := r.uploads
		r.mu.Unlock()
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%d", repo, id))
		w.WriteHeader(http.StatusAccepted)
	case http.MethodPut:
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		digest := req.URL.Query().Get("digest")
		if digest != sha256Digest(body) {
			http.Error(w, "digest mismatch", http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.blobs[digest] = body
		r.mu.Unlock()
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (r *fakeRegistry) blob(w http.ResponseWriter, req *http.Request, path string) {
	digest := path[strings.LastIndex(path, "/")+1:]
	r.mu.Lock()
	body, ok := r.blobs[digest]
	r.mu.Unlock()
	if !ok {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.Header().Set("Docker-Content-Digest", digest)
	if req.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

func (r *fakeRegistry) manifest(w http.ResponseWriter, req *http.Request, path string) {
	i := strings.Index(path, "/manifests/")
	repo, ref := path[:i], path[i+len("/manifests/"):]
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.Method == http.MethodPut {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		digest := sha256Digest(body)
		mediaType := req.Header.Get("Content-Type")
		if mediaType == "" {
			var m struct {
				MediaType string `json:"mediaType"`
			}
			_ = json.Unmarshal(body, &m)
			mediaType = m.MediaType
		}
		r.manifests[digest] = manifestEntry{mediaType: mediaType, body: body}
		if !strings.HasPrefix(ref, "sha256:") {
			if r.tags[repo] == nil {
				r.tags[repo] = map[string]string{}
			}
			r.tags[repo][ref] = digest
		}
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)
		return
	}
	digest := ref
	if !strings.HasPrefix(ref, "sha256:") {
		digest = r.tags[repo][ref]
	}
	m, ok := r.manifests[digest]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`))
		return
	}
	w.Header().Set("Content-Type", m.mediaType)
	w.Header().Set("Content-Length", fmt.Sprint(len(m.body)))
	w.Header().Set("Docker-Content-Digest", digest)
	if req.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(m.body)
}

func (r *fakeRegistry) tagList(w http.ResponseWriter, repo string) {
	r.mu.Lock()
	tags := make([]string, 0, len(r.tags[repo]))
	for tag := range r.tags[repo] {
		tags = append(tags, tag)
	}
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
}
