package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/Ricaardo/nimbus-os/news/internal/log"
	"github.com/Ricaardo/nimbus-os/news/internal/platformapp"
)

func main() {
	configPath := flag.String("config", "config.yaml", "config file path")
	apiPort := flag.Int("api-port", 8081, "API server port")
	flag.Parse()

	app, err := platformapp.New(platformapp.Options{
		ConfigPath:       *configPath,
		ListenAddr:       fmt.Sprintf("127.0.0.1:%d", *apiPort),
		BootstrapClosing: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "platform: %v\n", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "platform: %v\n", err)
		os.Exit(1)
	}
}
