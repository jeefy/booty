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

	tConfig "github.com/flatcar-linux/container-linux-config-transpiler/config"
	"github.com/flatcar-linux/container-linux-config-transpiler/config/types"
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

	macAddress := ""
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		log.Printf("Error splitting user ip: %v is not IP:port", r.RemoteAddr)
	}
	remoteIP := net.ParseIP(ip)

	if hwAddr, _, err := arping.Ping(remoteIP); err != nil {
		log.Printf("Error with ARP request: %s", err)
	} else {
		macAddress = hwAddr.String()
	}

	if r.URL.Query().Get("mac") != "" {
		macAddress = r.URL.Query().Get("mac")
	}

	if viper.GetBool("debug") {
		log.Printf("Using mac address `%s`", macAddress)
	}
	host := hardware.GetMacAddress(macAddress)
	if host == nil {
		//w.Write([]byte("Error retrieving host"))
		config := types.Config{}
		config.Systemd.Units = append(config.Systemd.Units, types.SystemdUnit{
			Name:   "Reboot now please",
			Enable: true,
			Contents: `
[Service]
Type=simple
ExecStart=reboot

[Install]
WantedBy=default.target`,
		})
		var dataOut []byte
		dataOut, err := json.Marshal(&config)
		if err != nil {
			w.Write([]byte(fmt.Sprintf("Failed to marshal output: %v", err)))
			return
		}
		w.Write(dataOut)
		return
	}

	templateData := struct {
		JoinString string
		ServerIP   string
	}{
		JoinString: viper.GetString(config.JoinString),
		ServerIP:   viper.GetString(config.ServerIP),
	}
	t, err := template.ParseFiles(fmt.Sprintf("%s/%s", viper.GetString(config.DataDir), viper.GetString(config.IgnitionFile)))
	if err != nil {
		w.Write([]byte(err.Error()))
		return
	}
	var tpl bytes.Buffer
	err = t.Execute(&tpl, templateData)
	if err != nil {
		w.Write([]byte(err.Error()))
	}

	cfg, ast, report := tConfig.Parse(tpl.Bytes())
	if len(report.Entries) > 0 {
		errMsg := fmt.Sprintf("Error parsing ignition: %s", report.String())
		log.Println(errMsg)
		if report.IsFatal() {
			w.Write([]byte(errMsg))
			return
		}
	}

	ignCfg, report := tConfig.Convert(cfg, "", ast)
	if len(report.Entries) > 0 {
		errMsg := fmt.Sprintf("Error converting ignition: %s", report.String())
		log.Println(errMsg)
		if report.IsFatal() {
			w.Write([]byte(errMsg))
			return
		}
	}

	var dataOut []byte
	dataOut, err = json.Marshal(&ignCfg)
	if err != nil {
		log.Printf("Failed to marshal output: %v", err)
	}

	w.Write(dataOut)
}

func handleVersionRequest(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.RequestURI, "json") {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{"version":"%s"}`, viper.GetString(config.CurrentVersion))))
		return
	}
	w.Write([]byte(fmt.Sprintf("FLATCAR_VERSION=%s", viper.GetString(config.CurrentVersion))))
}

func handleInfoRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(fmt.Sprintf(`{"flatcar":{"version":"%s"},"booty":{"version":"%s","timestamp":"%s"}}`, viper.GetString(config.CurrentVersion), viper.GetString("version"), viper.GetString("timestamp"))))
}
