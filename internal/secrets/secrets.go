package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

const credentialBytes = 32

// Credential returns a server-compatible credential: the supplied prefix followed
// by exactly 64 lowercase hexadecimal characters.
func Credential(source io.Reader, prefix string) (string, error) {
	if source == nil {
		source = rand.Reader
	}

	value, err := randomHex(source, credentialBytes)
	if err != nil {
		return "", fmt.Errorf("generate credential: %w", err)
	}
	return prefix + value, nil
}

// IdempotencyID returns a high-entropy identifier suitable for reuse across HTTP
// retries of one logical operation.
func IdempotencyID(source io.Reader) (string, error) {
	if source == nil {
		source = rand.Reader
	}
	return randomHex(source, credentialBytes)
}

func randomHex(source io.Reader, size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
