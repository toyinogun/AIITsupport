package runner

import (
	"testing"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

func TestTicketStateName(t *testing.T) {
	for in, want := range map[string]string{"new": "open", "open": "open", "Closed": "closed", "pending reminder": "pending reminder"} {
		if got := ticketStateName(in); got != want {
			t.Errorf("ticketStateName(%q) = %q, want %q", in, got, want)
		}
	}
}

// covers: AC-4 (spec 0018)
func TestGateTextSeen(t *testing.T) {
	if !gateTextSeen([]string{"Looking into it now.", agent.TextNotStaff}) || !gateTextSeen([]string{agent.TextNotMatched}) {
		t.Error("a gate text was not seen")
	}
	if gateTextSeen([]string{"Done."}) {
		t.Error("an ordinary reply counted as a gate text")
	}
}

func TestMailAndStaffID(t *testing.T) {
	m := directory.Model{Users: []directory.User{{Username: "a.b", Mail: "a.b@noord.example"}, {Username: "c.d", Mail: "c.d@noord.example"}}}
	if mail, ok := Mail(m, "c.d"); !ok || mail != "c.d@noord.example" {
		t.Errorf("Mail = %q, %v", mail, ok)
	}
	if id, ok := StaffID(m, "c.d"); !ok || id != "UEV0000002" {
		t.Errorf("StaffID = %q, %v", id, ok)
	}
	if _, ok := Mail(m, "nobody"); ok {
		t.Error("an unknown user has a mail")
	}
}
