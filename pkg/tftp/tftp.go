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

const (
	PXEConfigContents = `default flatcar
prompt 1
timeout 5

display boot.msg

label flatcar
	menu default
	kernel flatcar_production_pxe.vmlinuz
	initrd flatcar_production_pxe_image.cpio.gz
	append flatcar.first_boot=1 ignition.config.url=http://%s/config/flatcar.ign
`
)

// readHandler is called when client starts file download from server
func readHandler(filename string, rf io.ReaderFrom) error {
	log.Printf("TFTP Get: %s\n", filename)
	if filename == "pxelinux.cfg/default" {
		r := strings.NewReader(fmt.Sprintf(PXEConfigContents, viper.GetString(config.ServerIP)))
		rf.ReadFrom(r)
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
	s.SetTimeout(5 * time.Second) // optional
	go func() {
		err := s.ListenAndServe(":69") // blocks until s.Shutdown() is called
		if err != nil {
			log.Fatalf("TFTP Server error: %v\n", err)
		}
	}()
	log.Println("TFTP Server started")
}
