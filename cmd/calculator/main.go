package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/fufelshmerc-corparate/go_calc/internal/app"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load("conf.env"); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal(err)
	}
	config, err := app.ConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, config); err != nil {
		log.Fatal(err)
	}
}
