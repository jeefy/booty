package tftp

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/pin/tftp"
	"github.com/spf13/viper"
)

// readHandler is called when client starts file download from server
func readHandler(filename string, rf io.ReaderFrom) error {
	log.Printf("TFTP Get: %s\n", filename)
	raddr := rf.(tftp.OutgoingTransfer).RemoteAddr()
	laddr := rf.(tftp.RequestPacketInfo).LocalIP()
	if viper.GetBool("debug") {
		log.Println("RRQ from", raddr.String(), "To ", laddr.String())
		log.Println("")
	}

	osToLoad := "flatcar"
	menuDefault := "run-from-disk"

	if hwAddr, _, err := arping.Ping(raddr.IP); err != nil {
		log.Printf("Error with ARP request: %s", err)
	} else {
		macAddress := hwAddr.String()
		host := hardware.GetMacAddress(macAddress)
		if host != nil {
			if host.OS != "" {
				osToLoad = host.OS
			}
			if host.DoInstall {
				menuDefault = "install"
				if filename == "booty.ipxe" {
					host.DoInstall = false
					hardware.WriteMacAddress(macAddress, *host)
				}
			}
		}
	}

	urlHost := viper.GetString(config.ServerIP)
	hostPort := viper.GetInt(config.ServerHttpPort)
	if hostPort != 80 {
		urlHost = fmt.Sprintf("%s:%d", urlHost, hostPort)
	}

	if filename == "booty.ipxe" {
		toServe := strings.Replace(PXEConfig[fmt.Sprintf("%s.ipxe", osToLoad)], "[[server]]", urlHost, -1)
		toServe = strings.Replace(toServe, "[[menu-default]]", menuDefault, -1)
		toServe = strings.Replace(toServe, "[[coreos-channel]]", viper.GetString(config.CoreOSChannel), -1)
		toServe = strings.Replace(toServe, "[[coreos-arch]]", viper.GetString(config.CoreOSArchitecture), -1)
		toServe = strings.Replace(toServe, "[[coreos-version]]", viper.GetString(config.CurrentCoreOSVersion), -1)

		r := strings.NewReader(toServe)
		n, err := rf.ReadFrom(r)
		if err != nil {
			log.Printf("Error reading iPXE config: %v\n", err)
			return err
		}
		log.Printf("%d bytes sent (%s)\n", n, filename)
		return nil
	}

	if filename == "pxelinux.cfg/default" {
		r := strings.NewReader(strings.Replace(PXEConfig[osToLoad], "[[server]]", urlHost, -1))
		n, err := rf.ReadFrom(r)
		if err != nil {
			log.Printf("Error reading PXE config: %v\n", err)
			return err
		}
		log.Printf("%d bytes sent (%s)\n", n, filename)
		return nil
	}
	file, err := os.Open(fmt.Sprintf("%s/%s", viper.GetString(config.DataDir), filename))
	if err != nil {
		return err
	}
	n, err := rf.ReadFrom(file)
	if err != nil {
		return err
	}
	log.Printf("%d bytes sent (%s)\n", n, filename)
	return nil
}

// writeHandler is called when client starts file upload to server
func writeHandler(filename string, wt io.WriterTo) error {
	log.Printf("TFTP writes are not supported: %s\n", filename)
	return nil
}

func StartTFTP() {
	// use nil in place of handler to disable read or write operations
	s := tftp.NewServer(readHandler, writeHandler)
	s.SetBlockSize(512)
	s.EnableSinglePort()
	s.SetTimeout(60 * time.Second) // optional
	go func() {
		err := s.ListenAndServe(":69") // blocks until s.Shutdown() is called
		if err != nil {
			log.Fatalf("TFTP Server error: %v\n", err)
		}
	}()
	log.Println("TFTP Server started")
}
