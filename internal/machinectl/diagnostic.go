package machinectl

import (
	"regexp"
	"strings"
)

var diagnosticURL = regexp.MustCompile(`(?i)https?://[^\s]+`)
var diagnosticSecret = regexp.MustCompile(`(?i)(bearer\s+|sk-ac-|gh[pousr]_|(api[_-]?key|access|refresh|token|password|secret|authorization)\s*(?:[:=]\s*|\s+))[^\s,;]+`)
var diagnosticQuote = regexp.MustCompile(`"[^"\n]+"|'[^'\n]+'`)
var diagnosticLong = regexp.MustCompile(`[A-Za-z0-9_+/=-]{40,}`)

// safeMachineError is deliberately conservative: local errors can include
// credential URLs and quoted token material. Keep actionable prose such as
// `parsing time ""` while removing secrets before crossing the wire.
func safeMachineError(err error) string {
	if err == nil {
		return ""
	}
	reason := strings.Join(strings.Fields(err.Error()), " ")
	reason = diagnosticURL.ReplaceAllString(reason, "[url redacted]")
	reason = diagnosticSecret.ReplaceAllString(reason, "[redacted]")
	reason = diagnosticQuote.ReplaceAllString(reason, `"[redacted]"`)
	reason = diagnosticLong.ReplaceAllString(reason, "[redacted]")
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "Machine check failed."
	}
	r := []rune(reason)
	if len(r) > 300 {
		reason = string(r[:300])
	}
	return reason
}
