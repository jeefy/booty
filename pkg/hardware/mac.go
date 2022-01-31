package hardware

import (
	"encoding/json"
	"io/ioutil"
	"log"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

type Host struct {
	Hostname string `json:"hostname"`
}

var HostDB map[string]*Host

func init() {
	HostDB = make(map[string]*Host)
}

func GetMacAddress(mac string) *Host {
	data, err := ioutil.ReadFile(viper.GetString(config.DataDir) + "/" + viper.GetString(config.HardwareMap))
	if err != nil {
		log.Printf("Error reading hardware map: %s", err.Error())
		return nil
	}
	err = json.Unmarshal(data, &HostDB)
	if err != nil {
		log.Printf("Error unmarshalling hardware map: %s", err.Error())
		return nil
	}
	if val, ok := HostDB[mac]; ok {
		return val
	}

	return nil
}
