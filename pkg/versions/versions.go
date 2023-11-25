package versions

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-co-op/gocron"
	"github.com/jeefy/booty/pkg/config"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

func StartCron() {
	log.Println("Starting CRON version check")
	cron := gocron.NewScheduler(time.UTC)
	_, err := cron.Cron(viper.GetString(config.UpdateSchedule)).Do(VersionCheck)
	if err != nil {
		log.Fatalf("Error creating prune cronjob: %s", err.Error())
	}
	cron.StartAsync()
}

func VersionCheck() {
	if viper.GetBool(config.Updating) {
		log.Println("Already updating, skipping version check")
		return
	}
	if viper.GetBool("debug") {
		log.Println("Checking remote version")
	}

	if viper.GetString(config.CurrentVersion) == "" {
		// Check for an existing version.txt file
		if oldVer, err := os.Open(fmt.Sprintf("%s/version.txt", viper.GetString(config.DataDir))); err == nil {
			log.Println("Found old version.txt, setting current version to that")
			data, _ := godotenv.Parse(oldVer)
			if _, ok := data["FLATCAR_VERSION"]; !ok {
				log.Println("Old version.txt file is invalid")
				if err != nil {
					log.Println(err.Error())
				}
			}
			viper.Set(config.CurrentVersion, data["FLATCAR_VERSION"])
		} else {
			log.Printf("%s not found, setting current version to 0.0.0", fmt.Sprintf("%s/version.txt", viper.GetString(config.DataDir)))
			viper.Set(config.CurrentVersion, "0.0.0")
		}
	}

	LoadRemoteVersion()
	if viper.GetString(config.RemoteVersion) != viper.GetString(config.CurrentVersion) {
		viper.Set(config.Updating, true)
		log.Printf("Remote version %s is different than local version %s", viper.GetString(config.RemoteVersion), viper.GetString(config.CurrentVersion))

		if err := DownloadFlatcarFile("version.txt"); err != nil {
			log.Printf("Error downloading version.txt: %s", err.Error())
		}
		if err := DownloadFlatcarFile("flatcar_production_pxe_image.cpio.gz"); err != nil {
			log.Printf("Error downloading flatcar_production_pxe_image.cpio.gz: %s", err.Error())
		}
		if err := DownloadFlatcarFile("flatcar_production_pxe.vmlinuz"); err != nil {
			log.Printf("Error downloading flatcar_production_pxe.vmlinuz: %s", err.Error())
		}

		viper.Set(config.CurrentVersion, viper.GetString(config.RemoteVersion))
		viper.Set(config.Updating, false)
	}

}

func LoadRemoteVersion() {
	if resp, err := http.Get(RemoteFlatcarURL() + "/version.txt"); err == nil {
		data, _ := godotenv.Parse(resp.Body)
		if _, ok := data["FLATCAR_VERSION"]; !ok {
			log.Printf("Error retrieving remote version from %s", resp.Request.URL.String())
			if err != nil {
				log.Println(err.Error())
			}
			return
		}
		viper.Set(config.RemoteVersion, data["FLATCAR_VERSION"])
		if viper.GetBool("debug") {
			log.Printf("Remote version found: %s", data["FLATCAR_VERSION"])
		}
	} else {
		log.Printf("Error retrieving remote version from %s: %s", RemoteFlatcarURL(), err.Error())
	}
}

func RemoteFlatcarURL() string {
	return fmt.Sprintf(viper.GetString(config.FlatcarURL), viper.GetString(config.Channel), viper.GetString(config.Architecture))
}

func DownloadFlatcarFile(filename string) error {
	return config.DownloadFile(fmt.Sprintf(RemoteFlatcarURL()+"/%s", filename))
}
