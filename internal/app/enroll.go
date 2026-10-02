package app

import "github.com/AirCommand-AI/ac-cli/internal/enroll"

// enrollRequest preserves the CLI's existing transport, retries and error
// mapping while making registration/join available to the daemon as a package.
func (a *App) enrollRequest(method, path, token string, body []byte) (enroll.Response, error) {
	response, err := a.request(method, path, token, body)
	return enroll.Response{Status: response.status, Body: response.body}, err
}
