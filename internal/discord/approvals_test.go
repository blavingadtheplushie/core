package discord

import (
	"encoding/json"
	"strings"
	"testing"

	"blavingad/internal/core"
)

func TestApprovalButtons(t *testing.T) {
	b, _ := json.Marshal(buttons("apr_1", "shell", false))
	if strings.Contains(string(b), "apr:always:") || !strings.Contains(string(b), "apr:approve:apr_1") {
		t.Fatalf("non-admins must not get 'always': %s", b)
	}
	b, _ = json.Marshal(buttons("apr_1", "shell", true))
	if !strings.Contains(string(b), "apr:always:apr_1") {
		t.Fatalf("admins get 'always': %s", b)
	}
}

func TestApprovalText(t *testing.T) {
	s := approvalText(&core.Approval{Tool: "shell", Summary: "ls", Args: `{"command":"echo ` + "```" + `"}`, RequestedBy: "Paul"})
	if !strings.Contains(s, "`shell`") || strings.Count(s, "```") != 2 {
		t.Fatalf("bad text: %s", s)
	}
}
