package main

import (
	"net/http"
	"os"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/app"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
)

const dashboardURL = "https://dashboard.aircommand.ai"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		if err := writeVersion(os.Stdout); err != nil {
			_, _ = os.Stderr.WriteString("Unable to write version output.\n")
			os.Exit(1)
		}
		return
	}

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
		Store:       credentials.NewStore(home),
		ListenStore: listenstore.NewStore(home),
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	}
	os.Exit(client.Run(os.Args[1:]))
}
