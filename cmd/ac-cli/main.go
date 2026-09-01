package main

import (
	"net/http"
	"os"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/app"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

const dashboardURL = "https://dashboard.aircommand.ai"

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		_, _ = os.Stderr.WriteString("Unable to locate the home directory.\n")
		os.Exit(1)
	}

	client := &app.App{
		BaseURL: dashboardURL,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		Store:  credentials.NewStore(home),
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	os.Exit(client.Run(os.Args[1:]))
}
