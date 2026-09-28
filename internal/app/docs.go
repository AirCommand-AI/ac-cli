package app

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

const docLimit = 5 * 1024 * 1024

var docName = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

func (a *App) docRequest(workstream, agent, method, path string, payload any) ([]byte, error) {
	cred, err := a.credentialFor(workstream, agent)
	if err != nil {
		return nil, err
	}
	var body []byte
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	response, err := a.messageAPIRequestWithResponseLimit(method, "/agent/v1/workstreams/"+workstream+"/docs"+path, cred.APIToken, body, 32*1024*1024)
	if err != nil {
		return nil, err
	}
	if response.status < 200 || response.status >= 300 {
		if response.status == http.StatusConflict && responseCode(response.body) == "DocStale" {
			if input, ok := payload.(map[string]any); ok {
				return nil, &publicError{message: fmt.Sprintf("doc changed since rev %v; fetch and merge", input["baseRev"])}
			}
			return nil, &publicError{message: "doc changed; fetch and merge"}
		}
		if response.status == http.StatusTooManyRequests && responseCode(response.body) == "DocRateLimit" {
			return nil, &publicError{message: "Document rate limit exceeded (60 requests per minute)."}
		}
		return nil, workstreamResponseStatusError(response.status, response.body, workstream, method != http.MethodGet, cred)
	}
	return response.body, nil
}
func (a *App) docs(arguments []string) error {
	flags := flag.NewFlagSet("docs", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstream, agent string
	flags.StringVar(&workstream, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent")
	if flags.Parse(arguments) != nil || flags.NArg() != 0 || validateWorkstreamCode(workstream) != nil {
		return &publicError{message: "Usage: aircom docs --workstream <code> [--agent <agent>]"}
	}
	body, err := a.docRequest(workstream, agent, http.MethodGet, "", nil)
	if err != nil {
		return err
	}
	return writeSafeResponse(a.outputWriter(), body)
}
func (a *App) doc(arguments []string) error {
	if len(arguments) < 2 {
		return &publicError{message: docUsage}
	}
	action, name := arguments[0], arguments[1]
	if !docName.MatchString(name) {
		return &publicError{message: "Document name must be a lowercase slug (1–64 characters)."}
	}
	flags := flag.NewFlagSet("doc", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstream, agent, file, out, note string
	var rev, base, from, to int
	flags.StringVar(&workstream, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent")
	flags.StringVar(&file, "file", "", "markdown file")
	flags.StringVar(&out, "out", "", "output file")
	flags.StringVar(&note, "note", "", "revision note")
	flags.IntVar(&rev, "rev", 0, "revision")
	flags.IntVar(&base, "base-rev", -1, "revision read before edit")
	flags.IntVar(&from, "from", 0, "old revision")
	flags.IntVar(&to, "to", 0, "new revision")
	if flags.Parse(arguments[2:]) != nil || flags.NArg() != 0 || validateWorkstreamCode(workstream) != nil {
		return &publicError{message: docUsage}
	}
	path := "/" + url.PathEscape(name)
	switch action {
	case "get":
		if rev < 0 {
			return &publicError{message: "--rev must be positive."}
		}
		if rev > 0 {
			path += "?rev=" + strconv.Itoa(rev)
		}
		body, err := a.docRequest(workstream, agent, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		var result struct {
			Body     string `json:"body"`
			Revision struct {
				Rev int `json:"rev"`
			} `json:"revision"`
		}
		if json.Unmarshal(body, &result) != nil || result.Revision.Rev < 1 {
			return &publicError{message: "Invalid document response."}
		}
		if out != "" {
			if err = os.WriteFile(out, []byte(result.Body), 0600); err != nil {
				return &publicError{message: "Unable to write document output."}
			}
		} else {
			if _, err = io.WriteString(a.outputWriter(), result.Body); err != nil {
				return &publicError{message: "Unable to print document."}
			}
		}
		_, err = fmt.Fprintf(a.errorWriter(), "rev %d\n", result.Revision.Rev)
		return err
	case "put":
		if file == "" || base < 0 {
			return &publicError{message: "doc put requires --file and --base-rev (0 for a new document)."}
		}
		f, err := os.Open(filepath.Clean(file))
		if err != nil {
			return &publicError{message: "Unable to read document file."}
		}
		defer f.Close()
		content, err := io.ReadAll(io.LimitReader(f, docLimit+1))
		if err != nil {
			return &publicError{message: "Unable to read document file."}
		}
		if len(content) > docLimit {
			return &publicError{message: "Document exceeds 5 MB."}
		}
		body, err := a.docRequest(workstream, agent, http.MethodPut, path, map[string]any{"bodyBase64": base64.StdEncoding.EncodeToString(content), "baseRev": base, "note": note})
		if err != nil {
			return err
		}
		return writeSafeResponse(a.outputWriter(), body)
	case "diff":
		if from < 1 || to < 0 {
			return &publicError{message: "doc diff requires --from <positive revision>."}
		}
		query := url.Values{"from": {strconv.Itoa(from)}}
		if to > 0 {
			query.Set("to", strconv.Itoa(to))
		}
		body, err := a.docRequest(workstream, agent, http.MethodGet, path+"/diff?"+query.Encode(), nil)
		if err != nil {
			return err
		}
		var result struct {
			Diff string `json:"diff"`
		}
		if json.Unmarshal(body, &result) != nil {
			return &publicError{message: "Invalid diff response."}
		}
		_, err = io.WriteString(a.outputWriter(), result.Diff)
		return err
	case "archive":
		body, err := a.docRequest(workstream, agent, http.MethodPost, path+"/archive", nil)
		if err != nil {
			return err
		}
		return writeSafeResponse(a.outputWriter(), body)
	default:
		return &publicError{message: docUsage}
	}
}

const docUsage = "Usage: aircom docs --workstream <code> | doc get <name> --workstream <code> [--rev <n>] [--out <file>] | doc put <name> --workstream <code> --file <path> --base-rev <n> [--note <text>] | doc diff <name> --workstream <code> --from <n> [--to <n>] | doc archive <name> --workstream <code>"
