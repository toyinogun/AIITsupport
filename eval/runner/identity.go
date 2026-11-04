package runner

import (
	"fmt"

	"github.com/toyinogun/AIITsupport/eval/ticketset"
	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

// Fixed fake Slack identities (spec 0018, Value sourcing).
const (
	itApproverID    = "UEVIT00001"
	itApproverEmail = "it.desk@noord.example"
	guestID         = "UEVGUEST01"
	unmatchedID     = "UEVNOUSER1"
	unmatchedEmail  = "unknown.sender@noord.example"
	evalChannel     = "DEVAL0001"
	// ITChannel is the fixed fake id of the IT channel the agent posts cards to.
	ITChannel = "CEVALIT01"
)

// StaffID is a directory user's fake Slack id: UEV plus the 7 digit position
// of the user in noord.yaml, counted from 1.
func StaffID(m directory.Model, username string) (string, bool) {
	for i, u := range m.Users {
		if u.Username == username {
			return fmt.Sprintf("UEV%07d", i+1), true
		}
	}
	return "", false
}

// SlackUsers builds the fake users.info and users.lookupByEmail: every
// directory user, the fixed IT approver, a guest, and a confirmed address that
// is not in AD.
func SlackUsers(m directory.Model) *agenttest.Users {
	users := map[string]agent.SlackUser{
		itApproverID: withID(agenttest.Staff(itApproverEmail), itApproverID),
		guestID:      withID(agent.SlackUser{TeamID: agenttest.Team, IsRestricted: true, IsEmailConfirmed: true, Email: "guest@noord.example"}, guestID),
		unmatchedID:  withID(agenttest.Staff(unmatchedEmail), unmatchedID),
	}
	for _, u := range m.Users {
		id, _ := StaffID(m, u.Username)
		users[id] = withID(agenttest.Staff(u.Mail), id)
	}
	return agenttest.NewUsers(users)
}

func withID(u agent.SlackUser, id string) agent.SlackUser {
	u.ID = id
	return u
}

// SenderID is the Slack id a ticket's sender writes from.
func SenderID(m directory.Model, t ticketset.Ticket) (string, error) {
	switch t.Sender.Kind {
	case "guest":
		return guestID, nil
	case "unmatched_email":
		return unmatchedID, nil
	}
	id, ok := StaffID(m, t.Sender.User)
	if !ok {
		return "", fmt.Errorf("ticket %s: sender %q is not in the directory", t.ID, t.Sender.User)
	}
	return id, nil
}
