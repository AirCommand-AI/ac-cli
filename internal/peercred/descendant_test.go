//go:build darwin || linux

package peercred

import (
	"os"
	"testing"
)

func TestDescendantWalksRealAncestors(t *testing.T) {
	self, parent := os.Getpid(), os.Getppid()
	tests := []struct {
		name           string
		peer, ancestor int
		want           bool
	}{
		{name: "self", peer: self, ancestor: self, want: true},
		{name: "parent", peer: self, ancestor: parent, want: true},
		{name: "child is not an ancestor", peer: parent, ancestor: self, want: false},
		{name: "unrelated pid", peer: self, ancestor: 999999, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Descendant(test.peer, test.ancestor); got != test.want {
				t.Fatalf("Descendant(%d, %d) = %v, want %v", test.peer, test.ancestor, got, test.want)
			}
		})
	}
}
