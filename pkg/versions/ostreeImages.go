package versions

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/go-co-op/gocron"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func EnsureOCIFolders() {
	err := os.Mkdir(viper.GetString(config.DataDir)+"/registry/", 0755)
	if err != nil && !os.IsExist(err) {
		slog.Error("Error creating registry directory", "error", err)
		os.Exit(1)
	}
	err = os.Mkdir(viper.GetString(config.DataDir)+"/registry/blobs/", 0755)
	if err != nil && !os.IsExist(err) {
		slog.Error("Error creating registry blobs directory", "error", err)
		os.Exit(1)
	}
	err = os.Mkdir(viper.GetString(config.DataDir)+"/registry/blobs/sha256", 0755)
	if err != nil && !os.IsExist(err) {
		slog.Error("Error creating registry sha256 directory", "error", err)
		os.Exit(1)
	}
	symSrc, err := filepath.Abs(viper.GetString(config.DataDir) + "/registry/blobs/sha256")
	if err != nil {
		slog.Error("Error creating registry symlink abs path", "error", err)
		os.Exit(1)
	}
	err = os.Symlink(symSrc, viper.GetString(config.DataDir)+"/registry/sha256")
	if err != nil && !os.IsExist(err) {
		slog.Error("Error creating registry symlink", "error", err)
		os.Exit(1)
	}
}

func StartOSTreeImageSync() {
	slog.Info("Starting CRON version check for OCI Images")
	cron := gocron.NewScheduler(time.UTC)
	_, err := cron.Cron(viper.GetString(config.UpdateSchedule)).Do(OSTreeImageSync)
	if err != nil {
		slog.Error("Error creating OSTreeImageSync cronjob", "error", err)
		os.Exit(1)
	}
	cron.StartAsync()
}

func OSTreeImageSync() {
	EnsureOCIFolders()
	pulled := make(map[string]bool)
	bootyData := hardware.BootyData{}
	err := json.Unmarshal(hardware.GetData(), &bootyData)
	if err != nil {
		slog.Error("Error unmarshalling hardware map", "error", err)
		return
	}

	for _, host := range bootyData.Hosts {
		_, ok := pulled[host.OSTreeImage]
		if host.OSTreeImage != "" && !ok {
			ociImage := fmt.Sprintf("%s:%s/%s", viper.GetString(config.ServerIP), viper.GetString(config.HttpPort), host.OSTreeImage)
			//err := crane.Copy(host.OSTreeImage, ociImage, opts...)
			if err := OSTreeImagePull(host.OSTreeImage); err != nil {
				slog.Error("Error copying OCI image", "image", ociImage, "error", err)
				continue
			}
			slog.Info("Done copying OCI image", "image", ociImage)
			pulled[host.OSTreeImage] = true
		}
	}
}

func OSTreeImagePull(src string, opts ...crane.Option) error {
	o := crane.Options{
		Remote: []remote.Option{
			remote.WithAuthFromKeychain(authn.DefaultKeychain),
		},
		Keychain: authn.DefaultKeychain,
	}

	srcRef, err := name.ParseReference(src)
	if err != nil {
		return fmt.Errorf("parsing reference %q: %w", src, err)
	}

	img, err := remote.Image(srcRef, o.Remote...)
	if err != nil {
		return fmt.Errorf("fetching image %q: %w", srcRef, err)
	}

	slog.Info("Saving image", "ref", srcRef.String())

	err = crane.SaveOCI(img, viper.GetString(config.DataDir)+"/registry/")
	if err != nil {
		return fmt.Errorf("saving image %q: %w", srcRef, err)
	}

	localImage := fmt.Sprintf("%s:%s/%s", viper.GetString(config.ServerIP), viper.GetString(config.HttpPort), src)
	err = crane.Copy(src, localImage)
	if err != nil {
		return fmt.Errorf("error copying image %q: %w", srcRef, err)
	}

	slog.Info("Done saving image", "ref", srcRef.String())

	digest, err := crane.Digest(localImage)
	if err != nil {
		slog.Error("Error getting image from cache", "image", localImage, "error", err)
	}
	if digest == "" {
		slog.Warn("Image not found in local cache yet", "image", localImage)
	}

	return nil
}
