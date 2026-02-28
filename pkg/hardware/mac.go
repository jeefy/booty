package hardware

import (
	"encoding/json"
	"log"
	"os"
	"sync"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

type Host struct {
	MAC          string `json:"mac"`
	Hostname     string `json:"hostname"`
	IP           string `json:"ip"`
	Booted       string `json:"booted"`
	IgnitionFile string `json:"ignitionFile,omitempty"`
	OS           string `json:"os,omitempty"`
	OSTreeImage  string `json:"ostreeImage,omitempty"`
	DoInstall    bool   `json:"doInstall,omitempty"`
}

type BootyData struct {
	Hosts        map[string]*Host `json:"hosts"`
	UnknownHosts map[string]*Host `json:"unknownHosts"`
}

var HostDB map[string]*Host

// UnknownHosts uses sync.Map for lock-free reads on the write-once-read-many
// unknown-host lookup path. Keys are MAC address strings, values are *Host.
var UnknownHosts sync.Map

var fileMutex sync.Mutex

func init() {
	HostDB = make(map[string]*Host)
}

// unknownHostsSnapshot returns a plain map copy of UnknownHosts for JSON
// serialization and external consumers that expect map[string]*Host.
func unknownHostsSnapshot() map[string]*Host {
	m := make(map[string]*Host)
	UnknownHosts.Range(func(key, value interface{}) bool {
		m[key.(string)] = value.(*Host)
		return true
	})
	return m
}

func GetData() []byte {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	data, err := os.ReadFile(viper.GetString(config.DataDir) + "/" + viper.GetString(config.HardwareMap))
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
		UnknownHosts: unknownHostsSnapshot(),
	}

	output, err := json.Marshal(bd)
	if err != nil {
		log.Printf("Error marshalling hardware map: %s", err.Error())
		return nil
	}

	return output
}

func GetMacAddress(mac string) *Host {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	data, err := os.ReadFile(viper.GetString(config.DataDir) + "/" + viper.GetString(config.HardwareMap))
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
		UnknownHosts.Delete(mac)
		return val
	}

	UnknownHosts.Store(mac, &Host{})
	return nil
}

func WriteMacAddress(mac string, host Host) *Host {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	data, err := os.ReadFile(viper.GetString(config.DataDir) + "/" + viper.GetString(config.HardwareMap))
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
	err = os.WriteFile(viper.GetString(config.DataDir)+"/"+viper.GetString(config.HardwareMap), data, 0644)
	if err != nil {
		log.Printf("Error writing hardware map: %s", err.Error())
		return nil
	}

	UnknownHosts.Delete(mac)

	return &host
}

func RemoveMacAddress(mac string) {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	data, err := os.ReadFile(viper.GetString(config.DataDir) + "/" + viper.GetString(config.HardwareMap))
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
	err = os.WriteFile(viper.GetString(config.DataDir)+"/"+viper.GetString(config.HardwareMap), data, 0644)
	if err != nil {
		log.Printf("Error writing hardware map: %s", err.Error())
		return
	}
}
