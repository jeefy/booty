package tftp

import (
	"fmt"
	"io"
	"log/slog"
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
	slog.Info("TFTP Get", "filename", filename)
	raddr := rf.(tftp.OutgoingTransfer).RemoteAddr()
	laddr := rf.(tftp.RequestPacketInfo).LocalIP()
	slog.Debug("RRQ details", "from", raddr.String(), "to", laddr.String())

	osToLoad := "flatcar"
	menuDefault := "run-from-disk"

	if hwAddr, _, err := arping.Ping(raddr.IP); err != nil {
		slog.Error("Error with ARP request", "error", err)
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
			slog.Error("Error reading iPXE config", "error", err)
			return err
		}
		slog.Info("TFTP sent", "bytes", n, "filename", filename)
		return nil
	}

	if filename == "pxelinux.cfg/default" {
		r := strings.NewReader(strings.Replace(PXEConfig[osToLoad], "[[server]]", urlHost, -1))
		n, err := rf.ReadFrom(r)
		if err != nil {
			slog.Error("Error reading PXE config", "error", err)
			return err
		}
		slog.Info("TFTP sent", "bytes", n, "filename", filename)
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
	slog.Info("TFTP sent", "bytes", n, "filename", filename)
	return nil
}

// writeHandler is called when client starts file upload to server
func writeHandler(filename string, wt io.WriterTo) error {
	slog.Warn("TFTP writes are not supported", "filename", filename)
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
			slog.Error("TFTP Server error", "error", err)
			os.Exit(1)
		}
	}()
	slog.Info("TFTP Server started")
}
