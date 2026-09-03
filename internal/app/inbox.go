package app

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

const (
	inboxUsage = "Usage: ac-cli inbox --workstream <code> [--agent <agentId>] [--all] [--limit N] [--cursor C]"
	ackUsage   = "Usage: ac-cli ack --workstream <code> [--agent <agentId>] --message <messageId>"
)

type messageReadOperation int

const (
	messageListOperation messageReadOperation = iota
	messageAcknowledgeOperation
)

func (a *App) inbox(arguments []string) error {
	flags := flag.NewFlagSet("inbox", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstreamCode string
	var agentID string
	var all bool
	var limit string
	var cursor string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	flags.BoolVar(&all, "all", false, "list all workstream messages")
	flags.StringVar(&limit, "limit", "", "page size from 1 to 100")
	flags.StringVar(&cursor, "cursor", "", "opaque cursor from the same inbox mode")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || workstreamCode == "" {
		return &publicError{message: inboxUsage}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}
	if err := validateMessageLimit(limit); err != nil {
		return err
	}

	credential, err := a.credentialFor(workstreamCode, agentID)
	if err != nil {
		return err
	}
	mode := "unread"
	if all {
		mode = "all"
	}
	path := "/agent/v1/workstreams/" + workstreamCode + "/messages/" + mode
	query := make(url.Values)
	if limit != "" {
		query.Set("limit", limit)
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}

	response, err := a.messagePageRequest(path, credential.APIToken)
	if err != nil {
		return err
	}
	if response.status != http.StatusOK {
		return messageReadStatusError(response.status, response.body, messageListOperation, workstreamCode, "")
	}
	return writeSafeResponse(a.outputWriter(), response.body, credential.APIToken, credential.SocketKey)
}

func (a *App) ack(arguments []string) error {
	flags := flag.NewFlagSet("ack", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstreamCode string
	var agentID string
	var messageID string
	flags.StringVar(&workstreamCode, "workstream", "", "workstream code")
	flags.StringVar(&agentID, "agent", "", "agent ID")
	flags.StringVar(&messageID, "message", "", "message ID")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || workstreamCode == "" || messageID == "" {
		return &publicError{message: ackUsage}
	}
	if err := validateWorkstreamCode(workstreamCode); err != nil {
		return err
	}
	if !validMessageID(messageID) {
		return &publicError{message: "Message ID must be exactly 16 lowercase hexadecimal characters."}
	}

	credential, err := a.credentialFor(workstreamCode, agentID)
	if err != nil {
		return err
	}
	path := "/agent/v1/workstreams/" + workstreamCode + "/messages/" + messageID + "/acknowledge"
	response, err := a.messageAPIRequest(http.MethodPost, path, credential.APIToken, nil)
	if err != nil {
		return err
	}
	if response.status != http.StatusOK {
		return messageReadStatusError(response.status, response.body, messageAcknowledgeOperation, workstreamCode, messageID)
	}
	return writeSafeResponse(a.outputWriter(), response.body, credential.APIToken, credential.SocketKey)
}

func validateMessageLimit(limit string) error {
	if limit == "" {
		return nil
	}
	for _, character := range limit {
		if character < '0' || character > '9' {
			return &publicError{message: "Inbox limit must be an integer from 1 to 100."}
		}
	}
	value, err := strconv.Atoi(limit)
	if err != nil || value < 1 || value > 100 {
		return &publicError{message: "Inbox limit must be an integer from 1 to 100."}
	}
	return nil
}

func validMessageID(messageID string) bool {
	if len(messageID) != 16 {
		return false
	}
	for _, character := range messageID {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func messageReadStatusError(status int, body []byte, operation messageReadOperation, workstreamCode string, messageID string) error {
	response := serviceError(body)
	switch status {
	case http.StatusBadRequest:
		switch response.Code {
		case "InvalidMessagePagination":
			return &publicError{message: "Inbox limit must be an integer from 1 to 100."}
		case "InvalidMessageCursor":
			return &publicError{message: "The message cursor is invalid. Start again without --cursor, or use a cursor returned by the same inbox mode."}
		case "InvalidMessageID":
			return &publicError{message: "Message ID must be exactly 16 lowercase hexadecimal characters."}
		default:
			if operation == messageAcknowledgeOperation {
				return &publicError{message: "AirCommand rejected the message acknowledgement request as invalid."}
			}
			return &publicError{message: "AirCommand rejected the message listing request as invalid."}
		}
	case http.StatusUnauthorized:
		if operation == messageAcknowledgeOperation {
			return &publicError{message: "The agent is no longer authorized. Re-enroll it before acknowledging messages."}
		}
		return &publicError{message: "The agent is no longer authorized. Re-enroll it before listing messages."}
	case http.StatusNotFound:
		if response.Code == "MessageNotFound" {
			return &publicError{message: fmt.Sprintf("Message %s was not found or does not belong to this agent.", messageID)}
		}
		return &publicError{message: fmt.Sprintf("Workstream %s was not found or this agent is not bound to it.", workstreamCode)}
	case http.StatusRequestTimeout:
		if operation == messageAcknowledgeOperation {
			return &publicError{message: "Message acknowledgement could not be confirmed after retries. It is safe to repeat the ack command."}
		}
		return &publicError{message: "Message listing timed out after retries; no page was returned."}
	case http.StatusInternalServerError:
		if operation == messageAcknowledgeOperation {
			return &publicError{message: "AirCommand could not complete message acknowledgement after retries. It is safe to repeat the ack command."}
		}
		return &publicError{message: "AirCommand could not complete message listing after retries (HTTP 500)."}
	case http.StatusServiceUnavailable:
		switch response.Code {
		case "ServiceUnavailable":
			if operation == messageAcknowledgeOperation {
				return &publicError{message: "AirCommand authentication remained unavailable after retries. It is safe to repeat the ack command."}
			}
			return &publicError{message: "AirCommand authentication remained unavailable after retries; no message page was returned."}
		case "MessageReadUnavailable":
			return &publicError{message: "AirCommand could not complete message listing after retries; no page was returned."}
		case "MessageAcknowledgeUnavailable":
			return &publicError{message: "Message acknowledgement could not be confirmed after retries. It is safe to repeat the ack command."}
		default:
			if operation == messageAcknowledgeOperation {
				return &publicError{message: "AirCommand could not confirm message acknowledgement after retries (HTTP 503). It is safe to repeat the ack command."}
			}
			return &publicError{message: "AirCommand could not complete message listing after retries (HTTP 503)."}
		}
	default:
		if operation == messageAcknowledgeOperation {
			return &publicError{message: fmt.Sprintf("AirCommand message acknowledgement failed (HTTP %d).", status)}
		}
		return &publicError{message: fmt.Sprintf("AirCommand message listing failed (HTTP %d).", status)}
	}
}
