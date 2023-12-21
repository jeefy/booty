package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

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
