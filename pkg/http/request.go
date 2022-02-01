package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"text/template"

	tConfig "github.com/flatcar-linux/container-linux-config-transpiler/config"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func handleRequest(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Booty is up and running!"))
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
	templateData := struct {
		ServerIP string
	}{
		ServerIP: viper.GetString(config.ServerIP),
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
	conf, _, _ := tConfig.Parse(tpl.Bytes())
	data, err := json.Marshal(conf)
	if err != nil {
		w.Write([]byte(err.Error()))
		return
	}
	w.Write(data)
}

func handleVersionRequest(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(fmt.Sprintf("FLATCAR_VERSION=%s", viper.GetString(config.CurrentVersion))))
}
