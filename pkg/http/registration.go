package http

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/viper"
)

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

	if h.OSTreeImage != "" {
		ociImage := fmt.Sprintf("%s:%s/%s", viper.GetString(config.ServerIP), viper.GetString(config.HttpPort), h.OSTreeImage)

		go func() {
			err := versions.OSTreeImagePull(h.OSTreeImage)
			if err != nil {
				log.Printf("Error pulling %s: %s", ociImage, err.Error())
			}
		}()
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
