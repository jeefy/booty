package hardware

import (
	"encoding/json"
	"io/ioutil"
	"log"

	"sync"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

type Host struct {
	MAC      string `json:"mac"`
	Hostname string `json:"hostname"`
	IP       string `json:"ip"`
	Booted   string `json:"booted"`
}

type BootyData struct {
	Hosts        map[string]*Host `json:"hosts"`
	UnknownHosts map[string]*Host `json:"unknownHosts"`
}

var HostDB map[string]*Host
var UnknownHosts map[string]*Host
var fileMutex sync.Mutex

func init() {
	HostDB = make(map[string]*Host)
	UnknownHosts = make(map[string]*Host)
}

func GetData() []byte {
	fileMutex.Lock()
	defer fileMutex.Unlock()

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

	bd := BootyData{
		Hosts:        HostDB,
		UnknownHosts: UnknownHosts,
	}

	data, err = json.Marshal(bd)
	if err != nil {
		log.Printf("Error marshalling hardware map: %s", err.Error())
		return nil
	}

	return data
}

func GetMacAddress(mac string) *Host {
	fileMutex.Lock()
	defer fileMutex.Unlock()

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
		delete(UnknownHosts, mac)
		return val
	}

	UnknownHosts[mac] = &Host{}
	return nil
}

func WriteMacAddress(mac string, host Host) *Host {
	fileMutex.Lock()
	defer fileMutex.Unlock()

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
	HostDB[mac] = &host
	data, err = json.Marshal(HostDB)
	if err != nil {
		log.Printf("Error marshalling hardware map: %s", err.Error())
		return nil
	}
	err = ioutil.WriteFile(viper.GetString(config.DataDir)+"/"+viper.GetString(config.HardwareMap), data, 0644)
	if err != nil {
		log.Printf("Error writing hardware map: %s", err.Error())
		return nil
	}

	delete(UnknownHosts, mac)

	return &host
}

func RemoveMacAddress(mac string) {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	data, err := ioutil.ReadFile(viper.GetString(config.DataDir) + "/" + viper.GetString(config.HardwareMap))
	if err != nil {
		log.Printf("Error reading hardware map: %s", err.Error())
		return
	}
	err = json.Unmarshal(data, &HostDB)
	if err != nil {
		log.Printf("Error unmarshalling hardware map: %s", err.Error())
		return
	}
	delete(HostDB, mac)
	data, err = json.Marshal(HostDB)
	if err != nil {
		log.Printf("Error marshalling hardware map: %s", err.Error())
		return
	}
	err = ioutil.WriteFile(viper.GetString(config.DataDir)+"/"+viper.GetString(config.HardwareMap), data, 0644)
	if err != nil {
		log.Printf("Error writing hardware map: %s", err.Error())
		return
	}
}
