package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"text/template"

	butaneConfig "github.com/coreos/butane/config"
	butaneCommon "github.com/coreos/butane/config/common"
	coreOSType "github.com/coreos/ignition/v2/config/v3_5_experimental/types"
	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func handleRequest(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/", http.StatusFound)
}

func handleDataRequest(w http.ResponseWriter, r *http.Request) {
	w.Write(hardware.GetData())
}

func handleRegistrationRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Write([]byte("Incorrect method"))
		return
	}
	decoder := json.NewDecoder(r.Body)
	var h hardware.Host
	err := decoder.Decode(&h)
	if err != nil {
		w.Write([]byte(fmt.Sprintf("Error decoding JSON request: %s", err.Error())))
		return
	}

	hardware.WriteMacAddress(h.MAC, h)
	w.Write([]byte("OK"))
}

func handleUnregistrationRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Write([]byte("Incorrect method"))
		return
	}
	decoder := json.NewDecoder(r.Body)
	var h hardware.Host
	err := decoder.Decode(&h)
	if err != nil {
		w.Write([]byte(fmt.Sprintf("Error decoding JSON request: %s", err.Error())))
		return
	}

	hardware.RemoveMacAddress(h.MAC)
	w.Write([]byte("OK"))
}

func handleHostsRequest(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("mac") == "" {
		w.Write([]byte("MAC address is required"))
		return
	}

	host := hardware.GetMacAddress(r.URL.Query().Get("mac"))
	if host == nil {
		w.Write([]byte("Error retrieving host"))
		return
	}

	data, err := json.Marshal(host)
	if err != nil {
		w.Write([]byte("Error marshalling host data"))
		return
	}
	if len(data) > 0 {
		w.Write(data)
	}
}

func handleIgnitionRequest(w http.ResponseWriter, r *http.Request) {
	// If we don't identify the host, tell FlatCar to reboot
	// Reboot the host till we identify it
	// Cool so, we want to have logic based around a recognized MAC address
	// Therefore what we need to do is collect the MAC address

	log.Printf("Ignition Request URI: %s", r.RequestURI)

	macAddress := ""
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		log.Printf("Error splitting user ip: %v is not IP:port", r.RemoteAddr)
	}
	remoteIP := net.ParseIP(ip)

	if hwAddr, _, err := arping.Ping(remoteIP); err != nil {
		log.Printf("Error with ARP request: %s", err)
	} else {
		if viper.GetBool("debug") {
			log.Printf("Mac address from ARP `%s`", macAddress)
		}
		macAddress = hwAddr.String()
	}

	if r.URL.Query().Get("mac") != "" {
		macAddress = r.URL.Query().Get("mac")
		if viper.GetBool("debug") {
			log.Printf("Mac address url override `%s`", macAddress)
		}
	}

	if viper.GetBool("debug") {
		log.Printf("Using mac address `%s`", macAddress)
	}
	host := hardware.GetMacAddress(macAddress)

	var tpl bytes.Buffer

	templateData := struct {
		JoinString  string
		ServerIP    string
		OSTreeImage string
		Hostname    string
	}{
		JoinString: viper.GetString(config.JoinString),
		ServerIP:   viper.GetString(config.ServerIP),
	}

	ignitionFile := viper.GetString(config.IgnitionFile)
	if host != nil {
		if host.IgnitionFile != "" {
			ignitionFile = host.IgnitionFile
		}
		templateData.Hostname = host.Hostname
		templateData.OSTreeImage = host.OSTreeImage
	}
	t, err := template.ParseFiles(fmt.Sprintf("%s/%s", viper.GetString(config.DataDir), ignitionFile))
	if err != nil {
		w.Write([]byte(err.Error()))
		return
	}

	err = t.Execute(&tpl, templateData)
	if err != nil {
		w.Write([]byte(err.Error()))
		return
	}

	if host == nil {
		coreosConfig := coreOSType.Config{}
		coreosConfig.Ignition.Version = "3.4.0"
		truePointer := true
		contentsPointer := `
[Service]
Type=simple
ExecStart=reboot

[Install]
WantedBy=default.target
`
		coreosConfig.Systemd.Units = append(coreosConfig.Systemd.Units, coreOSType.Unit{
			Name:     "Reboot now please",
			Enabled:  &truePointer,
			Contents: &contentsPointer,
		})
		var dataOut []byte
		dataOut, err := json.Marshal(&coreosConfig)
		if err != nil {
			w.Write([]byte(fmt.Sprintf("Failed to marshal output: %v", err)))
			return
		}
		w.Write(dataOut)
		return
	}

	ignCfg, report, err := butaneConfig.TranslateBytes(tpl.Bytes(), butaneCommon.TranslateBytesOptions{
		Pretty: true,
	})
	if err != nil {
		errMsg := fmt.Sprintf("Error parsing coreos ignition: %s", err.Error())
		log.Println(errMsg)
		log.Printf("%s", tpl.Bytes())
		for _, entry := range report.Entries {
			log.Printf("%s", entry.String())
		}
		w.Write([]byte(errMsg))
		return
	}
	if len(report.Entries) > 0 {
		errMsg := fmt.Sprintf("Problems parsing coreos ignition: %s", report.String())
		log.Println(errMsg)
		log.Printf("%s", tpl.Bytes())
		for _, entry := range report.Entries {
			log.Printf("%s", entry.String())
		}
		if report.IsFatal() {
			w.Write([]byte(errMsg))
			return

		}
	}

	w.Write(ignCfg)
}

func handleVersionRequest(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.RequestURI, "json") {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{"version":"%s"}`, viper.GetString(config.CurrentFlatcarVersion))))
		return
	}
	w.Write([]byte(fmt.Sprintf("FLATCAR_VERSION=%s", viper.GetString(config.CurrentFlatcarVersion))))
}

func handleInfoRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(fmt.Sprintf(`{"flatcar":{"version":"%s"},"coreos":{"version":"%s"},"booty":{"version":"%s","timestamp":"%s"}}`, viper.GetString(config.CurrentFlatcarVersion), viper.GetString(config.CurrentCoreOSVersion), viper.GetString("version"), viper.GetString("timestamp"))))
}
