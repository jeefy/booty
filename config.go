package main

import (
	"fmt"
	"log"
	"os"

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
)

func LoadConfig(cmd *cobra.Command) {
	viper.SetDefault(Debug, false)
	viper.SetDefault(FlatcarURL, "https://%s.release.flatcar-linux.net/%s-usr/current")
	//https: //stable.release.flatcar-linux.net/amd64-usr/current/version.txt

	if file, err := os.Open(fmt.Sprintf("%s/version.txt", viper.GetString(DataDir))); err == nil {
		data, _ := godotenv.Parse(file)
		if _, ok := data["FLATCAR_VERSION"]; !ok {
			viper.Set(CurrentVersion, data["FLATCAR_VERSION"])
			log.Printf("Local version found: %s", data["FLATCAR_VERSION"])
		}
	} else {
		VersionCheck()
	}

	viper.BindEnv(RemoteVersion, "REMOTE_VERSION")
	viper.SetDefault(RemoteVersion, "")
	LoadRemoteVersion()

	viper.BindEnv(IgnitionFile, "IGNITION_FILE")
	viper.SetDefault(IgnitionFile, "ignition.yaml")

	viper.BindEnv(HardwareMap, "HARDWARE_MAP")
	viper.SetDefault(HardwareMap, "hardware.json")
}
