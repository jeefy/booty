package tftp

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/pin/tftp"
	"github.com/spf13/viper"
)

// readHandler is called when client starts file download from server
func readHandler(filename string, rf io.ReaderFrom) error {
	log.Printf("TFTP Get: %s\n", filename)
	if filename == "pxelinux.cfg/default" {
		r := strings.NewReader(fmt.Sprintf(PXEConfigContents, viper.GetString(config.ServerIP)))
		n, err := rf.ReadFrom(r)
		if err != nil {
			log.Printf("Error reading PXE config: %v\n", err)
			return err
		}
		log.Printf("%d bytes sent\n", n)
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
	log.Printf("%d bytes sent\n", n)
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
	s.SetBlockSize(2000)
	s.EnableSinglePort()
	s.SetTimeout(5 * time.Second) // optional
	go func() {
		err := s.ListenAndServe(":69") // blocks until s.Shutdown() is called
		if err != nil {
			log.Fatalf("TFTP Server error: %v\n", err)
		}
	}()
	log.Println("TFTP Server started")
}
