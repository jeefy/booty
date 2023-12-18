package versions

import (
	"encoding/json"
	"fmt"
	"log"
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
		log.Fatalf("Error creating registry directory: %s", err.Error())
	}
	err = os.Mkdir(viper.GetString(config.DataDir)+"/registry/blobs/", 0755)
	if err != nil && !os.IsExist(err) {
		log.Fatalf("Error creating registry directory: %s", err.Error())
	}
	err = os.Mkdir(viper.GetString(config.DataDir)+"/registry/blobs/sha256", 0755)
	if err != nil && !os.IsExist(err) {
		log.Fatalf("Error creating registry directory: %s", err.Error())
	}
	symSrc, err := filepath.Abs(viper.GetString(config.DataDir) + "/registry/blobs/sha256")
	if err != nil {
		log.Fatalf("Error creating registry symlink abs path: %s", err.Error())
	}
	err = os.Symlink(symSrc, viper.GetString(config.DataDir)+"/registry/sha256")
	if err != nil && !os.IsExist(err) {
		log.Fatalf("Error creating registry symlink: %s", err.Error())
	}
}

func StartOSTreeImageSync() {
	log.Println("Starting CRON version check for OCI Images")
	cron := gocron.NewScheduler(time.UTC)
	_, err := cron.Cron(viper.GetString(config.UpdateSchedule)).Do(OSTreeImageSync)
	if err != nil {
		log.Fatalf("Error creating OSTreeImageSync cronjob: %s", err.Error())
	}
	cron.StartAsync()
}

func OSTreeImageSync() {
	EnsureOCIFolders()
	pulled := make(map[string]bool)
	bootyData := hardware.BootyData{}
	err := json.Unmarshal(hardware.GetData(), &bootyData)
	if err != nil {
		log.Printf("Error unmarshalling hardware map: %s", err.Error())
		return
	}

	for _, host := range bootyData.Hosts {
		_, ok := pulled[host.OSTreeImage]
		if host.OSTreeImage != "" && !ok {
			ociImage := fmt.Sprintf("%s:%s/%s", viper.GetString(config.ServerIP), viper.GetString(config.HttpPort), host.OSTreeImage)
			//err := crane.Copy(host.OSTreeImage, ociImage, opts...)
			if err := OSTreeImagePull(host.OSTreeImage); err != nil {
				log.Printf("Error copying %s: %s", ociImage, err.Error())
				continue
			}
			log.Printf("Done copying %s", ociImage)
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

	log.Printf("Saving image %s", srcRef)

	err = crane.SaveOCI(img, viper.GetString(config.DataDir)+"/registry/")
	if err != nil {
		return fmt.Errorf("saving image %q: %w", srcRef, err)
	}

	localImage := fmt.Sprintf("%s:%s/%s", viper.GetString(config.ServerIP), viper.GetString(config.HttpPort), src)
	err = crane.Copy(src, localImage)
	if err != nil {
		return fmt.Errorf("error copying image %q: %w", srcRef, err)
	}

	log.Printf("Done saving image %s", srcRef)

	digest, err := crane.Digest(localImage)
	if err != nil {
		log.Printf("Error getting %s from cache: %s", localImage, err)
	}
	if digest == "" {
		log.Printf("Image (%s) not found in local cache yet...", localImage)
	}

	return nil
}
