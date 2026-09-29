// Package agentapi provides the shared agent notification wire format and
// wake composition used by both the interactive listener and the daemon.
package agentapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Notification struct {
	Type         string `json:"type"`
	MessageID    string `json:"messageId"`
	SenderID     string `json:"senderId"`
	SenderNature string `json:"senderNature"`
	Priority     string `json:"priority,omitempty"`
	At           string `json:"at"`
	Kind         string `json:"kind,omitempty"`
	TaskID       string `json:"taskId,omitempty"`
}

type SpooledNotification struct {
	Type         string `json:"type"`
	MessageID    string `json:"messageId"`
	SenderID     string `json:"senderId"`
	SenderNature string `json:"senderNature"`
	Priority     string `json:"priority,omitempty"`
	At           string `json:"at"`
	Kind         string `json:"kind,omitempty"`
	TaskID       string `json:"taskId,omitempty"`
	Summary      string `json:"summary"`
}

type Feed struct {
	Notifications    []Notification `json:"notifications"`
	Cursor           *string        `json:"cursor"`
	PollAfterSeconds *int           `json:"pollAfterSeconds"`
}

type SenderIdentity struct{ ID, Nature string }

// NotificationsPath preserves the distinction between no cursor (silent
// baseline) and an explicitly stored empty cursor.
func NotificationsPath(workstream, cursor string, hasCursor bool) string {
	path := "/agent/v1/workstreams/" + workstream + "/notifications"
	if hasCursor {
		path += "?" + (url.Values{"since": {cursor}}).Encode()
	}
	return path
}

// Fetch makes one request; callers own retry, credential refresh and cursor
// persistence. Only a successful feed response is decoded.
func Fetch(request func(method, path, token string, payload []byte) (int, []byte, error), workstream, token, cursor string, hasCursor bool) (Feed, int, []byte, error) {
	status, body, err := request(http.MethodGet, NotificationsPath(workstream, cursor, hasCursor), token, nil)
	if err != nil || status != http.StatusOK {
		return Feed{}, status, body, err
	}
	feed, decodeErr := DecodeFeed(body)
	return feed, status, body, decodeErr
}

func DecodeFeed(body []byte) (Feed, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var response struct {
		Notifications    []json.RawMessage `json:"notifications"`
		Cursor           *string           `json:"cursor"`
		PollAfterSeconds *int              `json:"pollAfterSeconds"`
	}
	if err := decoder.Decode(&response); err != nil {
		return Feed{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Feed{}, errors.New("unexpected data after notification response")
	}
	if response.Notifications == nil || response.Cursor == nil || response.PollAfterSeconds == nil {
		return Feed{}, errors.New("notification response is missing a required field")
	}
	feed := Feed{Notifications: make([]Notification, 0, len(response.Notifications)), Cursor: response.Cursor, PollAfterSeconds: response.PollAfterSeconds}
	for i, raw := range response.Notifications {
		var n Notification
		if err := json.Unmarshal(raw, &n); err != nil {
			log.Printf("agentapi: skipping malformed notification at index %d: invalid shape", i)
			continue
		}
		if err := validateNotification(n); err != nil {
			log.Printf("agentapi: skipping malformed notification at index %d: %v", i, err)
			continue
		}
		feed.Notifications = append(feed.Notifications, n)
	}
	return feed, nil
}

func validateNotification(n Notification) error {
	if n.Type != "message.received" || !validMessageID(n.MessageID) || strings.TrimSpace(n.SenderID) == "" || strings.TrimSpace(n.At) == "" {
		return errors.New("incomplete notification")
	}
	if n.SenderNature != "agent" && n.SenderNature != "human" {
		return errors.New("invalid sender nature")
	}
	if n.Priority != "" && n.Priority != "normal" && n.Priority != "urgent" {
		return errors.New("invalid priority")
	}
	switch n.Kind {
	case "task.assigned", "task.unassigned", "task.cancelled":
		if !validTaskID(n.TaskID) {
			return errors.New("task message without a valid task ID")
		}
	}
	return nil
}

func validMessageID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func validTaskID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func ComposeSummary(n Notification, workstream string, names map[SenderIdentity]string) string {
	sender := n.SenderID
	if name := names[SenderIdentity{ID: n.SenderID, Nature: n.SenderNature}]; name != "" {
		sender = name
	}
	urgency := ""
	if n.Priority == "urgent" {
		urgency = "URGENT "
	}
	switch n.Kind {
	case "task.assigned":
		return fmt.Sprintf("%sTask %s assigned to you by %s (%s) in workstream %s: %s; run aircom inbox.", urgency, n.TaskID, singleLine(sender), n.SenderNature, workstream, n.MessageID)
	case "task.unassigned":
		return fmt.Sprintf("%sTask %s reassigned away from you by %s (%s) in workstream %s: %s; run aircom inbox.", urgency, n.TaskID, singleLine(sender), n.SenderNature, workstream, n.MessageID)
	case "task.cancelled":
		return fmt.Sprintf("%sTask %s cancelled by %s (%s) in workstream %s: %s; run aircom inbox.", urgency, n.TaskID, singleLine(sender), n.SenderNature, workstream, n.MessageID)
	}
	label := "New message"
	if n.Priority == "urgent" {
		label = "URGENT message"
	}
	return fmt.Sprintf("%s from %s (%s) in workstream %s: %s; run aircom inbox.", label, singleLine(sender), n.SenderNature, workstream, n.MessageID)
}
func singleLine(value string) string {
	return strings.Map(func(c rune) rune {
		if c == '\r' || c == '\n' || c < 0x20 || c == 0x7f {
			return ' '
		}
		return c
	}, value)
}
func Spool(n Notification, summary string) SpooledNotification {
	return SpooledNotification{Type: n.Type, MessageID: n.MessageID, SenderID: n.SenderID, SenderNature: n.SenderNature, Priority: n.Priority, At: n.At, Kind: n.Kind, TaskID: n.TaskID, Summary: summary}
}

// TerminalStatus classifies lifecycle answers without changing the caller's
// presentation of recovery instructions. 409 is terminal only for the two
// explicit lifecycle codes; unrelated conflicts remain ordinary errors.
func TerminalStatus(status int, code string) bool {
	return status == http.StatusUnauthorized || status == http.StatusNotFound || status == http.StatusConflict && (code == "AgentStopped" || code == "AgentRemoved")
}
func Retryable(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusInternalServerError || status == http.StatusServiceUnavailable
}
func FailureReason(status int, code string) string {
	if status == http.StatusServiceUnavailable {
		switch code {
		case "ServiceUnavailable":
			return "AirCommand authentication service unavailable (HTTP 503)"
		case "NotificationFeedUnavailable":
			return "AirCommand notification feed unavailable (HTTP 503)"
		}
	}
	return fmt.Sprintf("AirCommand notification request failed (HTTP %d)", status)
}
func PollDelay(seconds *int) time.Duration {
	if seconds == nil {
		return 30 * time.Second
	}
	if *seconds < 5 {
		return 5 * time.Second
	}
	maximumSeconds := int64((time.Duration(1<<63 - 1)) / time.Second)
	if int64(*seconds) > maximumSeconds {
		return time.Duration(1<<63 - 1)
	}
	return time.Duration(*seconds) * time.Second
}
