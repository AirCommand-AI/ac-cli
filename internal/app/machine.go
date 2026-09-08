package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"

	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/secrets"
)

const (
	defaultDevicePollInterval = 5 * time.Second
	maxDevicePollDuration     = 10 * time.Minute
)

type startDeviceLoginRequest struct {
	MachineName string `json:"machineName"`
}

type startDeviceLoginResponse struct {
	UserCode        string `json:"userCode"`
	PollSecret      string `json:"pollSecret"`
	VerificationURL string `json:"verificationUrl"`
	ExpiresAt       string `json:"expiresAt"`
	PollInterval    int    `json:"pollIntervalSeconds"`
}

type pollDeviceLoginRequest struct {
	PollSecret string `json:"pollSecret"`
}

type pollDeviceLoginResponse struct {
	Status         string `json:"status"`
	Token          string `json:"token"`
	OrganizationID string `json:"organizationId"`
}

type workstreamSummary struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type listWorkstreamsResponse struct {
	Workstreams []workstreamSummary `json:"workstreams"`
	NextCursor  string              `json:"nextCursor"`
}

type joinRequest struct {
	AgentName     string `json:"agentName"`
	APIToken      string `json:"apiToken"`
	SocketKey     string `json:"socketKey"`
	IdempotencyID string `json:"idempotencyId"`
}

type joinResponse struct {
	AgentID        string `json:"agentId"`
	AgentName      string `json:"agentName"`
	WorkstreamCode string `json:"workstreamCode"`
	SocketAddress  string `json:"socketAddress"`
}

// login binds this machine to the operator's organization. It is the only
// command that needs a human, and it is needed once per machine.
func (a *App) login(arguments []string) error {
	if len(arguments) != 0 {
		return &publicError{message: "Usage: ac-cli login"}
	}
	if a.Store == nil {
		return &publicError{message: "Credential storage is unavailable."}
	}
	if err := a.Store.CheckLayout(); err != nil {
		return storageError(err, "Credential storage is unavailable.")
	}

	payload, err := json.Marshal(startDeviceLoginRequest{MachineName: machineName()})
	if err != nil {
		return &publicError{message: "Unable to prepare the login request."}
	}
	response, err := a.request(http.MethodPost, "/ajax/device/start", "", payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return &publicError{message: "Unable to start a login for this machine."}
	}
	var start startDeviceLoginResponse
	if err := json.Unmarshal(response.body, &start); err != nil || start.PollSecret == "" || start.UserCode == "" {
		return &publicError{message: "The login service returned an invalid response."}
	}

	fmt.Fprintf(a.outputWriter(), "Open %s and enter this code:\n\n    %s\n\nWaiting for approval...\n", start.VerificationURL, start.UserCode)

	interval := time.Duration(start.PollInterval) * time.Second
	if interval < defaultDevicePollInterval {
		interval = defaultDevicePollInterval
	}
	pollPayload, err := json.Marshal(pollDeviceLoginRequest{PollSecret: start.PollSecret})
	if err != nil {
		return &publicError{message: "Unable to prepare the login request."}
	}

	deadline := time.Now().Add(maxDevicePollDuration)
	for time.Now().Before(deadline) {
		a.sleep(interval)
		result, err := a.singleRequest(http.MethodPost, "/ajax/device/poll", "", pollPayload)
		if err != nil {
			continue
		}
		if result.status == http.StatusNotFound {
			return &publicError{message: "This login is no longer valid. Run ac-cli login again."}
		}
		if result.status < 200 || result.status >= 300 {
			return &publicError{message: "This login expired or was already used. Run ac-cli login again."}
		}
		var poll pollDeviceLoginResponse
		if err := json.Unmarshal(result.body, &poll); err != nil {
			return &publicError{message: "The login service returned an invalid response."}
		}
		if poll.Status == "pending" {
			continue
		}
		if poll.Token == "" || poll.OrganizationID == "" {
			return &publicError{message: "The login service returned an invalid response."}
		}
		if err := a.Store.SaveMachine(credentials.Machine{
			APIToken:       poll.Token,
			OrganizationID: poll.OrganizationID,
			CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			return &publicError{message: "Unable to store the machine credential."}
		}
		fmt.Fprintf(a.outputWriter(), "This machine is now logged in.\nRun ac-cli workstreams to see what it can join.\n")
		return nil
	}
	return &publicError{message: "The code was not approved in time. Run ac-cli login again."}
}

// workstreams lists what this machine can see. Seeing a workstream is not the
// same as being in it; joining is what allows messaging.
func (a *App) workstreams(arguments []string) error {
	if len(arguments) != 0 {
		return &publicError{message: "Usage: ac-cli workstreams"}
	}
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	response, err := a.request(http.MethodGet, "/v1/workstreams", machine.APIToken, nil)
	if err != nil {
		return err
	}
	if response.status == http.StatusUnauthorized {
		return &publicError{message: "This machine's login is no longer valid. Run ac-cli login again."}
	}
	if response.status < 200 || response.status >= 300 {
		return &publicError{message: "Unable to list workstreams."}
	}
	var list listWorkstreamsResponse
	if err := json.Unmarshal(response.body, &list); err != nil {
		return &publicError{message: "The AirCommand service returned an invalid response."}
	}
	joined := a.joinedWorkstreamCodes()
	writer := a.outputWriter()
	if len(list.Workstreams) == 0 {
		fmt.Fprintln(writer, "No workstreams in this organization.")
		return nil
	}
	for _, workstream := range list.Workstreams {
		marker := " "
		if _, ok := joined[workstream.Code]; ok {
			marker = "*"
		}
		fmt.Fprintf(writer, "%s %-8s %s\n", marker, workstream.Code, workstream.Name)
	}
	if len(joined) > 0 {
		fmt.Fprintln(writer, "\n* this machine already has an agent in this workstream")
	}
	return nil
}

// join creates an agent in a workstream and activates it. Its output matches
// exchange so that runtime adapters parse either identically.
func (a *App) join(arguments []string) error {
	flags := flag.NewFlagSet("join", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstreamCode string
	var agentName string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentName, "name", "", "name this agent takes in the workstream")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return &publicError{message: "Usage: ac-cli join --workstream <code> --name <agentName>"}
	}
	workstreamCode = strings.TrimSpace(workstreamCode)
	agentName = strings.TrimSpace(agentName)
	if workstreamCode == "" || agentName == "" {
		return &publicError{message: "Usage: ac-cli join --workstream <code> --name <agentName>"}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	if err := a.Store.CheckLayout(); err != nil {
		return storageError(err, "Credential storage is unavailable.")
	}

	random := a.randomReader()
	apiToken, err := secrets.Credential(random, "api_")
	if err != nil {
		return &publicError{message: "Unable to generate agent credentials."}
	}
	socketKey, err := secrets.Credential(random, "sock_")
	if err != nil {
		return &publicError{message: "Unable to generate agent credentials."}
	}
	idempotencyID, err := secrets.IdempotencyID(random)
	if err != nil {
		return &publicError{message: "Unable to generate a join idempotency ID."}
	}
	payload, err := json.Marshal(joinRequest{
		AgentName:     agentName,
		APIToken:      apiToken,
		SocketKey:     socketKey,
		IdempotencyID: idempotencyID,
	})
	if err != nil {
		return &publicError{message: "Unable to prepare the join request."}
	}

	response, err := a.request(http.MethodPost, "/v1/workstreams/"+workstreamCode+"/agents", machine.APIToken, payload)
	if err != nil {
		return err
	}
	switch {
	case response.status == http.StatusUnauthorized:
		return &publicError{message: "This machine's login is no longer valid. Run ac-cli login again."}
	case response.status == http.StatusNotFound:
		return &publicError{message: fmt.Sprintf("Workstream %s was not found in this organization.", workstreamCode)}
	case response.status == http.StatusBadRequest:
		return &publicError{message: joinRejectionMessage(response.body)}
	case response.status < 200 || response.status >= 300:
		return &publicError{message: "Unable to join that workstream."}
	}

	var joined joinResponse
	if err := json.Unmarshal(response.body, &joined); err != nil || joined.AgentID == "" {
		return &publicError{message: "The AirCommand service returned an invalid join response."}
	}
	if err := a.Store.Save(credentials.Credential{
		APIToken:       apiToken,
		SocketKey:      socketKey,
		WorkstreamCode: joined.WorkstreamCode,
		AgentID:        joined.AgentID,
		SocketAddress:  joined.SocketAddress,
	}); err != nil {
		return &publicError{message: "Joined the workstream but could not store the agent credential."}
	}

	writer := a.outputWriter()
	fmt.Fprintf(writer, "Agent ID: %s\n", joined.AgentID)
	fmt.Fprintf(writer, "Use for send/update/read/inbox/ack/listen: --agent %s\n", joined.AgentID)
	fmt.Fprintf(writer, "Agent name: %s\n", joined.AgentName)
	fmt.Fprintf(writer, "Workstream: %s\n", joined.WorkstreamCode)
	fmt.Fprintf(writer, "Socket address: %s\n", joined.SocketAddress)
	return nil
}

func (a *App) machineCredential() (credentials.Machine, error) {
	if a.Store == nil {
		return credentials.Machine{}, &publicError{message: "Credential storage is unavailable."}
	}
	machine, err := a.Store.LoadMachine()
	if err != nil {
		if err == credentials.ErrNoMachineLogin {
			return credentials.Machine{}, &publicError{message: "This machine is not logged in to AirCommand. Run ac-cli login."}
		}
		return credentials.Machine{}, &publicError{message: "Unable to read this machine's login."}
	}
	return machine, nil
}

// joinedWorkstreamCodes reports which workstreams already have a local agent.
// It reads only the non-secret workstream code from each stored credential.
func (a *App) joinedWorkstreamCodes() map[string]struct{} {
	codes := map[string]struct{}{}
	if a.Store == nil {
		return codes
	}
	for _, agent := range a.Store.ListLocalAgents() {
		codes[agent.WorkstreamCode] = struct{}{}
	}
	return codes
}

func (a *App) sleep(delay time.Duration) {
	if a.ListenSleep != nil {
		a.ListenSleep(delay)
		return
	}
	time.Sleep(delay)
}

func machineName() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

func joinRejectionMessage(body []byte) string {
	var payload struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if message := strings.TrimSpace(payload.Message); message != "" {
			return message
		}
		if message := strings.TrimSpace(payload.Error); message != "" {
			return message
		}
	}
	return "That join request was rejected."
}
