package pidriver

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FormatMessageGuidance mirrors adapters/pi/index.ts: the wake is a pointer,
// not the body. Keep reply/ack commands explicit and addressed by stable IDs.
func FormatMessageGuidance(cliPath, workstreamCode, agentID, summary, messageID, senderID string) string {
	base := shellQuote(cliPath)
	args := " --workstream " + shellQuote(workstreamCode) + " --agent " + shellQuote(agentID)
	mid, _ := json.Marshal(messageID)
	sid, _ := json.Marshal(senderID)
	return strings.Join([]string{
		"[AirCommand] " + summary,
		fmt.Sprintf("Pointer only, no body. messageId=%s senderId=%s", mid, sid),
		"Fetch: " + base + " inbox" + args,
		"Reply: " + base + " send" + args + " --to " + shellQuote(senderID) + " --body <shell-quoted-reply>",
		"Then ack, only after the action and reply both succeed: " + base + " ack" + args + " --message " + shellQuote(messageID),
	}, "\n")
}
func shellQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", `'"'"'`) + "'" }
