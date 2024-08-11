package versions

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/buger/jsonparser"
	"github.com/go-co-op/gocron"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func StartCoreOSCron() {
	log.Println("Starting CRON version check")
	cron := gocron.NewScheduler(time.UTC)
	_, err := cron.Cron(viper.GetString(config.UpdateSchedule)).Do(CoreOSVersionCheck)
	if err != nil {
		log.Fatalf("Error creating prune cronjob: %s", err.Error())
	}
	cron.StartAsync()
}

func CoreOSVersionCheck() {
	if viper.GetBool(config.Updating) {
		log.Println("Already updating, skipping version check")
		return
	}
	if viper.GetBool("debug") {
		log.Println("Checking remote coreos version")
	}

	if viper.GetString(config.CurrentCoreOSVersion) == "" {
		// Check for an existing coreos.json file
		if b, err := os.ReadFile(fmt.Sprintf("%s/%s.json", viper.GetString(config.DataDir), viper.GetString(config.CoreOSChannel))); err == nil {
			log.Println("Found old coreos json, setting current version to that")
			oldVersion, err := jsonparser.GetString(b, "architectures", viper.GetString(config.CoreOSArchitecture), "artifacts", "metal", "release")
			if err != nil {
				log.Printf("Old %s.json file is invalid", viper.GetString(config.CoreOSChannel))
				log.Println(err.Error())
			}
			viper.Set(config.CurrentCoreOSVersion, oldVersion)
			log.Printf("CoreOS version set to %s", oldVersion)
		} else {
			log.Printf("%s not found, setting current version to 0.0.0", fmt.Sprintf("%s/%s.json", viper.GetString(config.DataDir), viper.GetString(config.CoreOSChannel)))
			viper.Set(config.CurrentCoreOSVersion, "0.0.0")
		}
	}

	LoadRemoteCoreOSVersion()
	oldVersion := viper.GetString(config.CurrentCoreOSVersion)
	if viper.GetString(config.RemoteCoreOSVersion) != viper.GetString(config.CurrentCoreOSVersion) {
		viper.Set(config.Updating, true)
		log.Printf("Remote coreos version %s is different than local version %s", viper.GetString(config.RemoteCoreOSVersion), oldVersion)

		if err := DownloadCoreOSJSON(); err != nil {
			log.Printf("Error downloading coreos json: %s", err.Error())
		}
		toDownload := ""

		toDownload = fmt.Sprintf("fedora-coreos-%s-live-initramfs.%s.img", viper.GetString(config.RemoteCoreOSVersion), viper.GetString(config.CoreOSArchitecture))
		if err := DownloadCoreOSFile(toDownload); err != nil {
			log.Printf("Error downloading %s: %s", toDownload, err.Error())
		}

		toDownload = fmt.Sprintf("fedora-coreos-%s-live-kernel-%s", viper.GetString(config.RemoteCoreOSVersion), viper.GetString(config.CoreOSArchitecture))
		if err := DownloadCoreOSFile(toDownload); err != nil {
			log.Printf("Error downloading %s: %s", toDownload, err.Error())
		}

		toDownload = fmt.Sprintf("fedora-coreos-%s-live-rootfs.%s.img", viper.GetString(config.RemoteCoreOSVersion), viper.GetString(config.CoreOSArchitecture))
		if err := DownloadCoreOSFile(toDownload); err != nil {
			log.Printf("Error downloading %s: %s", toDownload, err.Error())
		}

		viper.Set(config.CurrentCoreOSVersion, viper.GetString(config.RemoteCoreOSVersion))

		// Remove old versions once new ones are downloaded
		os.Remove(fmt.Sprintf("fedora-coreos-%s-live-initramfs.%s.img", oldVersion, viper.GetString(config.CoreOSArchitecture)))
		os.Remove(fmt.Sprintf("fedora-coreos-%s-live-kernel-%s", oldVersion, viper.GetString(config.CoreOSArchitecture)))
		os.Remove(fmt.Sprintf("fedora-coreos-%s-live-rootfs.%s.img", oldVersion, viper.GetString(config.CoreOSArchitecture)))

		viper.Set(config.Updating, false)
	}

}

func LoadRemoteCoreOSVersion() {
	if resp, err := http.Get(RemoteCoreOSJSONURL()); err == nil {
		b, err := io.ReadAll(resp.Body)
		// b, err := ioutil.ReadAll(resp.Body)  Go.1.15 and earlier
		if err != nil {
			log.Println(err.Error())
		}

		remoteVersion, err := jsonparser.GetString(b, "architectures", viper.GetString(config.CoreOSArchitecture), "artifacts", "metal", "release")
		if err != nil {
			log.Printf("Error retrieving remote coreos version from %s", resp.Request.URL.String())
			log.Println(err.Error())
			return
		}
		viper.Set(config.RemoteCoreOSVersion, remoteVersion)
		if viper.GetBool("debug") {
			log.Printf("Remote coreos version found: %s", remoteVersion)
		}
	} else {
		log.Printf("Error retrieving remote coreos version from %s: %s", RemoteCoreOSURL(), err.Error())
	}
}

// https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/39.20231101.3.0/x86_64/fedora-coreos-39.20231101.3.0-live-kernel-x86_64
// https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/0.0.0/x86_64//fedora-coreos-39.20231101.3.0-live-kernel-x86_64
func RemoteCoreOSURL() string {
	return fmt.Sprintf(viper.GetString(config.CoreOSURL), viper.GetString(config.CoreOSChannel), viper.GetString(config.RemoteCoreOSVersion), viper.GetString(config.CoreOSArchitecture))
}

func DownloadCoreOSFile(filename string) error {
	return config.DownloadFile(fmt.Sprintf(RemoteCoreOSURL()+"/%s", filename))
}

func RemoteCoreOSJSONURL() string {
	return fmt.Sprintf("https://builds.coreos.fedoraproject.org/streams/%s.json", viper.GetString(config.CoreOSChannel))
}

func DownloadCoreOSJSON() error {
	return config.DownloadFile(RemoteCoreOSJSONURL())
}
