package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCommandTmuxActivityReadsDedicatedSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tmux")
	script := "#!/bin/sh\nif [ \"$1\" != -L ] || [ \"$2\" != aircom ]; then exit 12; fi\ncase \"$3\" in\n list-clients) printf 'eng-1\\n' ;;\n list-windows) printf '1760000000\\n' ;;\n *) exit 14 ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	attached, last, err := (CommandTmux{Path: path}).Activity(context.Background(), []string{"eng-1"})
	if err != nil || !attached || !last.Equal(time.Unix(1760000000, 0)) {
		t.Fatalf("activity %v %v %v", attached, last, err)
	}
}
