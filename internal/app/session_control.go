package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/daemonclient"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

type SessionControl interface {
	Status(context.Context) (daemonclient.Status, error)
	ClaimSession(context.Context, string, string) (daemonclient.SessionClaim, error)
	AttachSession(context.Context, daemonclient.SessionAttach) error
	SubscribeSession(context.Context, int, func(daemonclient.SessionMessage) error) error
	SessionEvent(context.Context, int, string, string) error
}

func (a *App) sessionControl() SessionControl {
	if a.SessionClient != nil {
		return a.SessionClient
	}
	return daemonclient.Client{SocketPath: storagepath.DaemonSocket(a.Store.Home())}
}
func (a *App) ensureDaemon(client SessionControl) error {
	if _, err := client.Status(context.Background()); err == nil {
		return nil
	}
	if err := a.daemonCommand([]string{"start"}); err != nil {
		return &publicError{message: fmt.Sprintf("Unable to start the machine daemon: %v", err)}
	}
	for attempt := 0; attempt < 10; attempt++ {
		if _, err := client.Status(context.Background()); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return &publicError{message: "Machine daemon did not start. Ask a person to check aircom daemon status and the daemon log."}
}
func (a *App) attachSession(claim daemonclient.SessionClaim, agentID, agentName, workstream string) error {
	discovered, err := discoverSession(os.Getpid(), a.ProcessSnapshot)
	if err != nil {
		return err
	}
	discovered.AgentID = agentID
	discovered.Name = agentName
	discovered.Workstream = workstream
	if err := claim.AttachSession(discovered); err != nil {
		return &publicError{message: fmt.Sprintf("Unable to attach this program to agent %s: %v", agentID, err)}
	}
	return nil
}
