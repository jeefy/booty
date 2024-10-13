package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"text/template"

	butaneConfig "github.com/coreos/butane/config"
	butaneCommon "github.com/coreos/butane/config/common"
	coreOSType "github.com/coreos/ignition/v2/config/v3_5_experimental/types"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

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
		ServerIP:   fmt.Sprintf("%s:%s", viper.GetString(config.ServerIP), viper.GetString(config.ServerHttpPort)),
	}

	ignitionFile := viper.GetString(config.IgnitionFile)
	if host != nil {
		if host.IgnitionFile != "" {
			ignitionFile = host.IgnitionFile
		}
		templateData.Hostname = host.Hostname

		// Ingelligently rewrite what image to send to the client depending on cache state
		// First, default to the remote image location
		templateData.OSTreeImage = host.OSTreeImage

		// If we have a local image, use that instead
		if host.OSTreeImage != "" {
			localImage := fmt.Sprintf("%s:%s/%s", viper.GetString(config.ServerIP), viper.GetString(config.HttpPort), host.OSTreeImage)
			digest, err := crane.Digest(localImage)
			if err != nil {
				log.Printf("Error getting %s from cache: %s", localImage, err)
			}
			if digest == "" {
				log.Printf("Image (%s) not found in local cache yet...", localImage)
			} else {
				templateData.OSTreeImage = localImage
			}
		}
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
