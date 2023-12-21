package http

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

type Image struct {
	Registry string `json:"registry"`
	Image    string `json:"image"`
	Tag      string `json:"tag"`
	Digest   string `json:"digest"`
	UpToDate bool   `json:"upToDate"`
}

func handleRegistryRequest(w http.ResponseWriter, r *http.Request) {
	imageList := []Image{}
	registry := fmt.Sprintf("%s:%s", viper.GetString(config.ServerIP), viper.GetString(config.HttpPort))
	images, err := crane.Catalog(registry)
	if err != nil {
		w.Write([]byte(fmt.Sprintf("Error getting catalog from %s: %s", registry, err.Error())))
		return
	}
	for _, image := range images {
		tags, err := crane.ListTags(fmt.Sprintf("%s/%s", registry, image))
		if err != nil {
			w.Write([]byte(fmt.Sprintf("Error getting tags for %s: %s", image, err.Error())))
			return
		}
		for _, tag := range tags {
			cacheDesc, err := crane.Get(fmt.Sprintf("%s/%s:%s", registry, image, tag))
			if err != nil {
				continue
			}

			remoteDesc, err := crane.Get(fmt.Sprintf("%s:%s", image, tag))
			if err != nil {
				continue
			}

			imageList = append(imageList, Image{
				Registry: registry,
				Image:    image,
				Tag:      tag,
				Digest:   cacheDesc.Digest.String(),
				UpToDate: cacheDesc.Digest == remoteDesc.Digest,
			})
		}
	}

	v, err := json.Marshal(imageList)
	if err != nil {
		w.Write([]byte(fmt.Sprintf("Error marshalling image list: %s", err.Error())))
		return
	}

	w.Header().Set("Content-Type", "application/json")

	w.Write(v)
}
