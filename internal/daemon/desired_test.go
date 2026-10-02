package daemon

import (
	"context"
	"testing"
	"time"
)

type desiredSupervisor struct {
	fakeSupervisor
	posted []string
}

func (s *desiredSupervisor) PostDesired(_ context.Context, name string) error {
	s.posted = append(s.posted, name)
	return nil
}

func TestLocalLifecyclePostsDesiredThroughDaemon(t *testing.T) {
	s := &desiredSupervisor{}
	for _, req := range []Request{
		{Op: "agent.start", Name: "eng-1", AgentID: "agm_1", Organization: "org_a", Workstream: "348", WorkFolder: "/tmp/work", Mode: "tmux"},
		{Op: "agent.stop", Name: "eng-1"},
		{Op: "agent.mode", Name: "eng-1", Mode: "headless"},
	} {
		response := dispatch(context.Background(), s, req, time.Time{}, "dev", "", 0, nil)
		if !response.OK {
			t.Fatalf("request %+v: %+v", req, response)
		}
	}
	if len(s.posted) != 3 {
		t.Fatalf("posts: %v", s.posted)
	}
}
