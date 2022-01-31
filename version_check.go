package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"time"

	"github.com/go-co-op/gocron"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

func StartCron() {
	log.Println("Starting CRON version check")
	cron := gocron.NewScheduler(time.UTC)
	_, err := cron.Cron(args.cronSchedule).Do(VersionCheck)
	if err != nil {
		log.Fatalf("Error creating prune cronjob: %s", err.Error())
	}
	cron.StartAsync()
}

func VersionCheck() {
	log.Println("Checking remote version")
	LoadRemoteVersion()
	if viper.GetString(RemoteVersion) != viper.GetString(CurrentVersion) {
		log.Printf("Remote version %s is different than local version %s", viper.GetString(RemoteVersion), viper.GetString(CurrentVersion))

		if err := DownloadFlatcarFile(fmt.Sprintf("version.txt")); err != nil {
			log.Printf("Error downloading version.txt: %s", err.Error())
		}
		if err := DownloadFlatcarFile(fmt.Sprintf("flatcar_production_pxe_image.cpio.gz")); err != nil {
			log.Printf("Error downloading flatcar_production_pxe_image.cpio.gz: %s", err.Error())
		}
		if err := DownloadFlatcarFile(fmt.Sprintf("flatcar_production_pxe.vmlinuz")); err != nil {
			log.Printf("Error downloading flatcar_production_pxe.vmlinuz: %s", err.Error())
		}

		viper.Set(CurrentVersion, viper.GetString(RemoteVersion))
	}

}

func LoadRemoteVersion() {
	if resp, err := http.Get(RemoteFlatcarURL() + "/version.txt"); err == nil {
		data, _ := godotenv.Parse(resp.Body)
		if _, ok := data["FLATCAR_VERSION"]; !ok {
			log.Printf("Error retrieving remote version from %s", RemoteFlatcarURL())
			if err != nil {
				log.Println(err.Error())
			}
			return
		}
		viper.Set(RemoteVersion, data["FLATCAR_VERSION"])
		log.Printf("Remote version found: %s", data["FLATCAR_VERSION"])
	} else {
		log.Printf("Error retrieving remote version from %s: %s", RemoteFlatcarURL(), err.Error())
	}
}

func RemoteFlatcarURL() string {
	return fmt.Sprintf(viper.GetString(FlatcarURL), viper.GetString(Channel), viper.GetString(Architecture))
}

func DownloadFlatcarFile(filename string) error {
	return DownloadFile(fmt.Sprintf(RemoteFlatcarURL()+"/%s", filename))
}

func DownloadFile(url string) error {
	log.Printf("Downloading %s", url)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	f, err := os.Create(fmt.Sprintf("%s/%s", viper.GetString(DataDir), path.Base(resp.Request.URL.Path)))
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	if err != nil {
		return err
	}

	return nil
}
