package main

import (
	"github.com/IvanSolomatin/calc_v2/internal/app"
	"log"
	"os"
	"os/signal"
	"syscall"
	"github.com/joho/godotenv"
)

func main() {
	err := godotenv.Load("./conf.env")
	if err != nil {
		log.Printf("failed to load env: %v", err)
	}
	go func() {
		app.RunAgentGRPC()
	}()

	app.RunOrchestrator()
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	log.Println("Shutting down...")
}
