package secrets

import (
	"crypto/rand"
	"regexp"
	"testing"
)

func TestCredentialFormat(t *testing.T) {
	t.Parallel()

	for _, prefix := range []string{"api_", "sock_"} {
		value, err := Credential(rand.Reader, prefix)
		if err != nil {
			t.Fatalf("Credential(%q): %v", prefix, err)
		}
		if matched := regexp.MustCompile("^" + prefix + `[0-9a-f]{64}$`).MatchString(value); !matched {
			t.Fatalf("credential %q does not have the required format", value)
		}
	}
}

func TestIdempotencyIDFormat(t *testing.T) {
	t.Parallel()

	value, err := IdempotencyID(rand.Reader)
	if err != nil {
		t.Fatalf("IdempotencyID: %v", err)
	}
	if matched := regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(value); !matched {
		t.Fatalf("idempotency ID %q does not have the expected format", value)
	}
}
