package app

import (
	"context"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/daemonclient"
)

type fakeSessionControl struct {
	pid           int
	kind, logical string
	events        int
	claims        int
	attaches      []daemonclient.SessionAttach
	messages      []daemonclient.SessionMessage
}

func (*fakeSessionControl) Status(context.Context) (daemonclient.Status, error) {
	return daemonclient.Status{}, nil
}

type fakeClaim struct{ parent *fakeSessionControl }

func (*fakeClaim) Close() error { return nil }
func (c *fakeClaim) AttachSession(s daemonclient.SessionAttach) error {
	c.parent.attaches = append(c.parent.attaches, s)
	return nil
}
func (f *fakeSessionControl) ClaimSession(context.Context, string, string) (daemonclient.SessionClaim, error) {
	f.claims++
	return &fakeClaim{parent: f}, nil
}
func (f *fakeSessionControl) AttachSession(_ context.Context, s daemonclient.SessionAttach) error {
	f.attaches = append(f.attaches, s)
	return nil
}
func (f *fakeSessionControl) SubscribeSession(_ context.Context, _ int, handle func(daemonclient.SessionMessage) error) error {
	for _, m := range f.messages {
		if err := handle(m); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeSessionControl) SessionEvent(_ context.Context, pid int, kind, logical string) error {
	f.pid = pid
	f.kind = kind
	f.logical = logical
	f.events++
	return nil
}

func TestStateReportsOnlyToDaemon(t *testing.T) {
	client, _, stderr := testApp(t, "http://127.0.0.1:1", "", nil)
	daemon := &fakeSessionControl{}
	client.SessionClient = daemon
	client.ProcessSnapshot = func(pid int) (int, string, string, error) {
		return 1, "node /opt/pi-coding-agent/dist/cli.js", "start", nil
	}
	if code := client.Run([]string{"state", "--source", "other", "working"}); code != 0 {
		t.Fatalf("state failed: %s", stderr.String())
	}
	if daemon.events != 1 || daemon.kind != "state" || daemon.logical != "working" || daemon.pid <= 0 {
		t.Fatalf("event: %+v", daemon)
	}
	if code := client.Run([]string{"state", "stalled"}); code == 0 || daemon.events != 1 {
		t.Fatal("invalid state reached daemon")
	}
}
