package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

// Upstream endpoint keys. Every one is a plain base URL without a trailing
// slash; the defaults are the public services.
const (
	GithubAPIURL              = "githubAPIURL"
	GithubDownloadURL         = "githubDownloadURL"
	FedoraMirrorURL           = "fedoraMirrorURL"
	FedoraReleases            = "fedoraReleases"
	AllowUnverifiedFedoraRPMs = "allowUnverifiedFedoraRPMs"
	CoreOSStreamsURL          = "coreOSStreamsURL"

	DefaultGithubAPIURL      = "https://api.github.com"
	DefaultGithubDownloadURL = "https://github.com"
	DefaultFedoraMirrorURL   = "https://dl.fedoraproject.org/pub/fedora/linux"
	DefaultFlatcarURL        = "https://%s.release.flatcar-linux.net/%s-usr/%s"
	DefaultCoreOSURL         = "https://builds.coreos.fedoraproject.org/prod/streams/%s/builds/%s/%s"
	DefaultCoreOSStreamsURL  = "https://builds.coreos.fedoraproject.org/streams/%s.json"
)

func setEndpointDefaults() {
	viper.SetDefault(GithubAPIURL, DefaultGithubAPIURL)
	viper.SetDefault(GithubDownloadURL, DefaultGithubDownloadURL)
	viper.SetDefault(FedoraMirrorURL, DefaultFedoraMirrorURL)
	viper.SetDefault(FedoraReleases, "")
	viper.SetDefault(AllowUnverifiedFedoraRPMs, false)
	viper.SetDefault(CoreOSStreamsURL, DefaultCoreOSStreamsURL)
}

func baseURL(key, def string) string {
	if v := strings.TrimRight(strings.TrimSpace(viper.GetString(key)), "/"); v != "" {
		return v
	}
	return def
}

// GithubAPIBase is --githubAPIURL (GitHub or GitHub Enterprise REST API).
func GithubAPIBase() string { return baseURL(GithubAPIURL, DefaultGithubAPIURL) }

// GithubDownloadBase is --githubDownloadURL, the host release assets are
// downloaded from (<base>/<owner>/<repo>/releases/download/...).
func GithubDownloadBase() string { return baseURL(GithubDownloadURL, DefaultGithubDownloadURL) }

// FedoraMirrorBase is --fedoraMirrorURL, the root of a Fedora mirror tree
// (<base>/updates/<NN>/Everything/...).
func FedoraMirrorBase() string { return baseURL(FedoraMirrorURL, DefaultFedoraMirrorURL) }

// FedoraReleaseList is the Fedora releases whose trees are searched, newest
// first: --fedoraReleases (comma separated) when set, otherwise derived from
// the .fcNN suffix of --fedoraGrubVersion as NN+1 and NN, so it moves along
// with the GRUB pin instead of going stale.
func FedoraReleaseList() []string {
	var out []string
	for _, r := range strings.Split(viper.GetString(FedoraReleases), ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	if len(out) > 0 {
		return out
	}
	grub := viper.GetString(FedoraGrubVersion)
	if grub == "" {
		grub = DefaultFedoraGrubVersion
	}
	if i := strings.LastIndex(grub, ".fc"); i >= 0 {
		if n, err := strconv.Atoi(grub[i+3:]); err == nil && n > 0 {
			return []string{strconv.Itoa(n + 1), strconv.Itoa(n)}
		}
	}
	return []string{"45", "44"}
}

// ValidateEndpoints checks the upstream URL settings: --flatcarURL and
// --coreOSURL must be http(s) templates with exactly three %s verbs and
// --coreOSStreamsURL with exactly one (and no other verbs), the base URLs must parse as http(s) URLs and --fedoraReleases
// may only list numbers. Call it once after the flags are parsed.
func ValidateEndpoints() error {
	for _, key := range []string{FlatcarURL, CoreOSURL} {
		if err := validateURLTemplate(key, viper.GetString(key), 3); err != nil {
			return err
		}
	}
	if err := validateURLTemplate(CoreOSStreamsURL, viper.GetString(CoreOSStreamsURL), 1); err != nil {
		return err
	}
	for _, key := range []string{GithubAPIURL, GithubDownloadURL, FedoraMirrorURL} {
		v := viper.GetString(key)
		if v == "" {
			continue
		}
		if err := validateHTTPURL(key, v); err != nil {
			return err
		}
	}
	for _, r := range strings.Split(viper.GetString(FedoraReleases), ",") {
		if r = strings.TrimSpace(r); r != "" && strings.Trim(r, "0123456789") != "" {
			return fmt.Errorf("--%s: %q is not a Fedora release number", FedoraReleases, r)
		}
	}
	return nil
}

func validateURLTemplate(key, tmpl string, verbs int) error {
	n := 0
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '%' {
			continue
		}
		if i+1 >= len(tmpl) {
			return fmt.Errorf("--%s %q: trailing %%", key, tmpl)
		}
		switch tmpl[i+1] {
		case '%':
		case 's':
			n++
		default:
			return fmt.Errorf("--%s %q: only %%s verbs are allowed, found %%%c", key, tmpl, tmpl[i+1])
		}
		i++
	}
	if n != verbs {
		return fmt.Errorf("--%s %q: needs exactly %d %%s verbs, has %d", key, tmpl, verbs, n)
	}
	args := make([]any, verbs)
	for i := range args {
		args[i] = "x"
	}
	return validateHTTPURL(key, fmt.Sprintf(tmpl, args...))
}

func validateHTTPURL(key, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("--%s %q: %w", key, raw, err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("--%s %q: must be an http(s) URL", key, raw)
	}
	return nil
}
