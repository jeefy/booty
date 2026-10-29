package versions

import (
	"compress/gzip"
	"context"
	"crypto"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeefy/booty/pkg/config"
	"github.com/klauspost/compress/zstd"
)

// errNotInRepo means the repository answered but does not carry the
// package (or does not exist at that path): try the next mirror path.
var errNotInRepo = errors.New("not in repository")

// errRepomdMissing means the path has no repodata/repomd.xml (404): the
// package cannot be verified there, though it may still exist.
var errRepomdMissing = errors.New("repository metadata (repomd.xml) missing")

// fedoraRepoOf splits a package URL ".../Packages/<l>/<name>.rpm" into the
// repository base (ending in "/") and the location href primary.xml uses.
func fedoraRepoOf(pkgURL string) (base, href string, ok bool) {
	i := strings.LastIndex(pkgURL, "/Packages/")
	if i < 0 {
		return "", "", false
	}
	return pkgURL[:i+1], pkgURL[i+1:], true
}

type repomd struct {
	Data []struct {
		Type     string `xml:"type,attr"`
		Checksum struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"checksum"`
		Location struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
	} `xml:"data"`
}

type primaryPackage struct {
	Checksum struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"checksum"`
	Location struct {
		Href string `xml:"href,attr"`
	} `xml:"location"`
}

// fedoraRepoSHA256 resolves the sha256 of the package at pkgURL from its
// repository's metadata: repodata/repomd.xml names primary.xml and its
// sha256, primary.xml (verified against it) names the package's sha256.
// The chain is anchored in the HTTPS mirror the package comes from. A 404
// on repomd.xml is errRepomdMissing.
func fedoraRepoSHA256(ctx context.Context, pkgURL string) (string, error) {
	base, href, ok := fedoraRepoOf(pkgURL)
	if !ok {
		return "", fmt.Errorf("%s: not a repository package path", pkgURL)
	}
	mdURL := base + "repodata/repomd.xml"
	body, err := fetchSmall(ctx, mdURL, 1<<20)
	if errors.Is(err, errNotInRepo) {
		return "", fmt.Errorf("%s: %w", mdURL, errRepomdMissing)
	}
	if err != nil {
		return "", err
	}
	var md repomd
	if err := xml.Unmarshal(body, &md); err != nil {
		return "", fmt.Errorf("%s: %w", mdURL, err)
	}
	var primaryHref, primarySum string
	for _, d := range md.Data {
		if d.Type == "primary" {
			if d.Checksum.Type != "sha256" {
				return "", fmt.Errorf("%s: primary checksum type %q is not sha256", mdURL, d.Checksum.Type)
			}
			primaryHref, primarySum = d.Location.Href, strings.TrimSpace(d.Checksum.Value)
		}
	}
	if primaryHref == "" || primarySum == "" || strings.Contains(primaryHref, "..") {
		return "", fmt.Errorf("%s: no usable primary metadata", mdURL)
	}

	tmpDir, err := os.MkdirTemp("", "booty-repodata-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	primaryPath := filepath.Join(tmpDir, filepath.Base(primaryHref))
	if err := config.Download(ctx, config.DownloadClient, base+primaryHref, primaryPath, crypto.SHA256, primarySum); err != nil {
		return "", err
	}
	return findPrimarySHA256(primaryPath, href)
}

func findPrimarySHA256(path, href string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer config.CloseQuietly(f, path)
	var r io.Reader = f
	switch {
	case strings.HasSuffix(path, ".gz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		defer config.CloseQuietly(gz, path)
		r = gz
	case strings.HasSuffix(path, ".zst"):
		dec, err := zstd.NewReader(f)
		if err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		defer dec.Close()
		r = dec
	case strings.HasSuffix(path, ".xml"):
	default:
		return "", fmt.Errorf("%s: unsupported primary metadata compression", filepath.Base(path))
	}
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return "", fmt.Errorf("%s: %w", href, errNotInRepo)
		}
		if err != nil {
			return "", fmt.Errorf("primary metadata: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "package" {
			continue
		}
		var p primaryPackage
		if err := dec.DecodeElement(&p, &se); err != nil {
			return "", fmt.Errorf("primary metadata: %w", err)
		}
		if p.Location.Href != href {
			continue
		}
		sum := strings.ToLower(strings.TrimSpace(p.Checksum.Value))
		if p.Checksum.Type != "sha256" || len(sum) != 64 {
			return "", fmt.Errorf("%s: primary metadata has no sha256 (%s)", href, p.Checksum.Type)
		}
		return sum, nil
	}
}

// fetchSmall GETs url (bounded by limit) through the metadata client; 404
// is errNotInRepo.
func fetchSmall(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := config.MetadataClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	defer config.CloseQuietly(resp.Body, url)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s: %w", url, errNotInRepo)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}
