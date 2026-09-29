package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

var socketSecretPattern = regexp.MustCompile(`^msock_[a-f0-9]{64}$`)

// MachineSecret uses the issued secret on a newly registered device; existing
// machines rotate/fetch once with their machine token and persist it at 0600.
func MachineSecret(ctx context.Context, store *credentials.Store, client *http.Client, baseURL string) (string, error) {
	machine, err := store.LoadMachine()
	if err != nil {
		return "", err
	}
	if machine.MachineSocketSecret != "" {
		if !socketSecretPattern.MatchString(machine.MachineSocketSecret) {
			return "", errors.New("invalid stored machine socket secret")
		}
		return machine.MachineSocketSecret, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/machine/socket-secret", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+machine.APIToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := safeClient.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("machine socket secret request failed (HTTP %d)", response.StatusCode)
	}
	var result struct {
		MachineSocketSecret string `json:"machineSocketSecret"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result); err != nil {
		return "", err
	}
	if !socketSecretPattern.MatchString(result.MachineSocketSecret) {
		return "", errors.New("invalid machine socket secret response")
	}
	machine.MachineSocketSecret = result.MachineSocketSecret
	if err := store.SaveMachine(machine); err != nil {
		return "", err
	}
	return result.MachineSocketSecret, nil
}
