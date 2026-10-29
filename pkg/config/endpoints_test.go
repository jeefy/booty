package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestFedoraReleaseList(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	LoadConfig()
	viper.Set(FedoraGrubVersion, "2.12-40.fc44")
	if got := FedoraReleaseList(); !reflect.DeepEqual(got, []string{"45", "44"}) {
		t.Errorf("derived = %v", got)
	}
	viper.Set(FedoraGrubVersion, "weird")
	if got := FedoraReleaseList(); !reflect.DeepEqual(got, []string{"45", "44"}) {
		t.Errorf("fallback = %v", got)
	}
	viper.Set(FedoraReleases, " 47, ,46 ")
	if got := FedoraReleaseList(); !reflect.DeepEqual(got, []string{"47", "46"}) {
		t.Errorf("explicit = %v", got)
	}
}

func TestEndpointBases(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	LoadConfig()
	if GithubAPIBase() != DefaultGithubAPIURL || GithubDownloadBase() != DefaultGithubDownloadURL || FedoraMirrorBase() != DefaultFedoraMirrorURL {
		t.Error("defaults not applied")
	}
	viper.Set(GithubAPIURL, "https://ghe.example/api/v3/")
	if got := GithubAPIBase(); got != "https://ghe.example/api/v3" {
		t.Errorf("trailing slash kept: %q", got)
	}
}

func TestValidateEndpoints(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	LoadConfig()
	if err := ValidateEndpoints(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	for _, tc := range []struct{ key, val, want string }{
		{FlatcarURL, "https://x/%s/%s", "exactly 3"},
		{FlatcarURL, "https://x/%s/%s/%s/%s", "exactly 3"},
		{CoreOSURL, "https://x/%s/%d/%s", "only %s"},
		{CoreOSURL, "ftp://x/%s/%s/%s", "http(s)"},
		{GithubAPIURL, "not a url", "http(s)"},
		{FedoraReleases, "45,rawhide", "release number"},
		{CoreOSStreamsURL, "https://x/streams.json", "exactly 1"},
	} {
		viper.Reset()
		LoadConfig()
		viper.Set(tc.key, tc.val)
		if err := ValidateEndpoints(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s=%q: %v", tc.key, tc.val, err)
		}
	}
	viper.Reset()
	LoadConfig()
	viper.Set(FlatcarURL, "https://x/a%%20b/%s/%s/%s")
	if err := ValidateEndpoints(); err != nil {
		t.Errorf("escaped %%: %v", err)
	}
}
