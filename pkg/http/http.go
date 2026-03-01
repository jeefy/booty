package http

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func StartHTTP() {
	port := fmt.Sprintf(":%d", viper.GetInt(config.HttpPort))
	slog.Info("Starting HTTP server", "port", port)
	// Create a mux for routing incoming requests
	myHandler := http.NewServeMux()

	// All URLs will be handled by this function
	myHandler.HandleFunc("/", handleRequest)
	myHandler.HandleFunc("/ignition.json", handleIgnitionRequest)
	myHandler.HandleFunc("/version.txt", handleVersionRequest)
	myHandler.HandleFunc("/version.json", handleVersionRequest)
	myHandler.HandleFunc("/hosts", handleHostsRequest)
	myHandler.HandleFunc("/register", handleRegistrationRequest)
	myHandler.HandleFunc("/unregister", handleUnregistrationRequest)
	myHandler.HandleFunc("/booty.json", handleDataRequest)
	myHandler.HandleFunc("/info", handleInfoRequest)
	myHandler.HandleFunc("/registry", handleRegistryRequest)
	myHandler.Handle("/data/", http.StripPrefix("/data/", http.FileServer(http.Dir(viper.GetString(config.DataDir)))))
	myHandler.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.Dir("./web/dist"))))

	ociRegistry := registry.New(registry.WithBlobHandler(registry.NewDiskBlobHandler(viper.GetString(config.DataDir) + "/registry")))

	myHandler.Handle("/v2/", ociRegistry)

	s := &http.Server{
		Addr:           port,
		Handler:        logRequest(myHandler),
		ReadTimeout:    900 * time.Second,
		WriteTimeout:   900 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP listen failed", "error", err)
			os.Exit(1)
		}
	}()
	slog.Info("Server Started")

	<-done
	slog.Info("Server Stopped")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() {
		// extra handling here
		cancel()
	}()

	if err := s.Shutdown(ctx); err != nil {
		slog.Error("Server Shutdown Failed", "error", err)
		os.Exit(1)
	}
	slog.Info("Server Exited Properly")
}

func logRequest(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Don't log OCI registry requests
		if !strings.Contains(r.URL.Path, "/v2/") {
			slog.Info("HTTP request", "remote", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
		}
		handler.ServeHTTP(w, r)
	})
}
