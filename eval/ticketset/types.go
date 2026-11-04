// Package ticketset loads and checks the eval's fixed list of 50 tickets
// (spec 0017). The harness reads only this package; the agent never imports
// it, because it pulls in the directory truth from internal/seed.
package ticketset

// Ticket is one eval case: who writes, what they write, and what the agent
// should end up doing.
type Ticket struct {
	ID       string   `yaml:"id"`
	Category string   `yaml:"category"`
	Tags     []string `yaml:"tags"`
	Sender   Sender   `yaml:"sender"`
	Message  string   `yaml:"message"`
	Setup    []string `yaml:"setup"`
	Approval string   `yaml:"approval"`
	Expected Expected `yaml:"expected"`
	Expect   Expect   `yaml:"expect"`
	Why      string   `yaml:"why"`
}

// Sender is who sends the ticket.
type Sender struct {
	Kind string `yaml:"kind"`
	User string `yaml:"user"`
}

// Expected is the outcome and the ordered tools the agent should use.
type Expected struct {
	Outcome        string   `yaml:"outcome"`
	Tools          []string `yaml:"tools"`
	ForbiddenTools []string `yaml:"forbidden_tools"`
}

// Expect holds the optional extra checks.
type Expect struct {
	FinalTicketState string   `yaml:"final_ticket_state"`
	KBReview         *bool    `yaml:"kb_review"`
	RefusalCode      string   `yaml:"refusal_code"`
	ReplyMustNot     []string `yaml:"reply_must_not"`
}

// Set is a loaded, validated ticket file.
type Set struct {
	Tickets []Ticket
	// Hash is the sha256 of the file bytes exactly as read.
	Hash string
	// AsOf is the fiction's "today"; it becomes KB_STALE_AS_OF for a run.
	AsOf string
}

// ByCategory counts the tickets per category.
func (s Set) ByCategory() map[string]int {
	counts := map[string]int{}
	for _, t := range s.Tickets {
		counts[t.Category]++
	}
	return counts
}

// file mirrors the YAML shape.
type file struct {
	Version int      `yaml:"version"`
	AsOf    string   `yaml:"as_of"`
	Tickets []Ticket `yaml:"tickets"`
}
