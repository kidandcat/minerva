package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

// Password is set at build time with -ldflags "-X main.Password=secret"
var Password string

var (
	serverURL  string
	agentName  string
	workingDir string
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	flag.StringVar(&serverURL, "server", "ws://localhost:8081/agent", "Minerva WebSocket server URL")
	flag.StringVar(&agentName, "name", "", "Agent name (defaults to hostname)")
	flag.StringVar(&workingDir, "dir", "", "Working directory (defaults to current dir)")
	flag.Parse()

	// Default agent name to hostname
	if agentName == "" {
		hostname, err := os.Hostname()
		if err != nil {
			log.Fatalf("Failed to get hostname: %v", err)
		}
		agentName = hostname
	}

	// Default working directory to current dir
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			log.Fatalf("Failed to get working directory: %v", err)
		}
		workingDir = cwd
	}

	// Make working directory absolute
	absDir, err := filepath.Abs(workingDir)
	if err != nil {
		log.Fatalf("Failed to resolve working directory: %v", err)
	}
	workingDir = absDir

	if Password == "" {
		log.Fatalf("Binary not configured: password not set. Build with: go build -ldflags \"-X main.Password=SECRET\"")
	}

	log.Printf("Starting minerva-agent")
	log.Printf("  Name: %s", agentName)
	log.Printf("  Server: %s", serverURL)
	log.Printf("  Working dir: %s", workingDir)

	// Create and start client
	client := NewClient(serverURL, agentName, workingDir, Password)

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Shutting down...")
		client.Close()
		os.Exit(0)
	}()

	// Connect and run
	if err := client.Run(); err != nil {
		log.Fatalf("Client error: %v", err)
	}
}
