package config

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	CurrentVersion = "currentVersion"
	RemoteVersion  = "remoteVersion"
	Channel        = "channel"
	IgnitionFile   = "ignitionFile"
	HardwareMap    = "hardwareMap"
	Architecture   = "architecture"
	Debug          = "debug"
	UpdateSchedule = "updateSchedule"
	HttpPort       = "httpPort"
	DataDir        = "dataDir"
	FlatcarURL     = "flatcarURL"
	ServerIP       = "serverIP"
	ServerHttpPort = "serverHttpPort"
	JoinString     = "joinString"
	Updating       = "updating"
)

func LoadConfig(cmd *cobra.Command) {
	viper.SetDefault(Debug, false)
	viper.SetDefault(Updating, false)
	viper.SetDefault(FlatcarURL, "https://%s.release.flatcar-linux.net/%s-usr/current")
	//https: //stable.release.flatcar-linux.net/amd64-usr/current/version.txt

	if file, err := os.Open(fmt.Sprintf("%s/version.txt", viper.GetString(DataDir))); err == nil {
		data, _ := godotenv.Parse(file)
		if _, ok := data["FLATCAR_VERSION"]; !ok {
			viper.Set(CurrentVersion, data["FLATCAR_VERSION"])
			log.Printf("Local version found: %s", data["FLATCAR_VERSION"])
		}
	} else {
		log.Printf("Error retrieving existing local version: %s", err.Error())
	}

	viper.BindEnv(IgnitionFile, "IGNITION_FILE")
	viper.SetDefault(IgnitionFile, "config/ignition.yaml")

	viper.BindEnv(HardwareMap, "HARDWARE_MAP")
	viper.SetDefault(HardwareMap, "hardware.json")
}

func DownloadFile(url string) error {
	log.Printf("Downloading %s", url)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	filename := fmt.Sprintf("%s/%s", viper.GetString(DataDir), path.Base(url))
	log.Printf("Creating %s", filename)

	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	if err != nil {
		return err
	}
	fileInfo, err := f.Stat()
	if err != nil {
		return err
	}
	log.Printf("Download completed for %s (%d)", url, fileInfo.Size())

	return nil
}

func EnsureDeps() {
	DownloadFile("http://ftp.us.debian.org/debian/dists/stable/main/installer-amd64/20230607/images/netboot/pxelinux.0")
	DownloadFile("http://ftp.us.debian.org/debian/dists/stable/main/installer-amd64/20230607/images/netboot/debian-installer/amd64/boot-screens/ldlinux.c32")
	DownloadFile("https://raw.githubusercontent.com/jeefy/booty/main/undionly.kpxe")
}
