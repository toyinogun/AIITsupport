package agent

import "testing"

// covers: spec 0009 AC-9 and AC-16 (the name rule).
func TestRedactRequester(t *testing.T) {
	cases := []struct {
		name, summary, displayName, department, want string
	}{
		{
			"full name and department", "Jan de Vries (Operations) asked for admin.", "Jan de Vries", "Operations",
			"[name] ([department]) asked for admin.",
		},
		{
			"parts, case, punctuation", "jan's laptop; ask Vries, OPERATIONS knows.", "Jan de Vries", "Operations",
			"[name]'s laptop; ask [name], [department] knows.",
		},
		{
			"word containing a part is kept", "Since January, Jan waits.", "Jan de Vries", "Operations",
			"Since January, [name] waits.",
		},
		{"short part kept", "de printer is broken", "Jan de Vries", "Operations", "de printer is broken"},
		{
			"hyphenated name", "Eva and Marie and Eva-Marie asked.", "Eva-Marie Peters", "Sales",
			"[name] and [name] and [name] asked.",
		},
		{"straight apostrophe", "D'Souza and Souza asked.", "Ravi D'Souza", "IT", "[name] and [name] asked."},
		{"curly apostrophe", "D’Souza asked.", "Ravi D’Souza", "IT", "[name] asked."},
		{"decomposed diacritic", "Özkan asked.", "Mehmet Özkan", "Finance", "[name] asked."},
		{"department with symbols", "Someone in (R&D) asked.", "Jan de Vries", "R&D", "Someone in ([department]) asked."},
		{"placeholder not matched again", "Name asked.", "Name Person", "Dept", "[name] asked."},
		{"department holding a name part", "Vries Group asked.", "Jan de Vries", "Vries Group", "[department] asked."},
		{
			"whitespace inside stored values", "Jan de Vries from Sales Ops asked.", "Jan  de\tVries", "Sales   Ops",
			"[name] from [department] asked.",
		},
		{"empty values", "Jan asked.", "", "", "Jan asked."},
		{
			"two letter department kept", "Add me to SG-IT-Admins; only IT can grant it.", "Ravi D'Souza", "IT",
			"Add me to SG-IT-Admins; only IT can grant it.",
		},
		{
			"department inside a group name kept", "Add me to SG-Transport, Transport needs it.", "Jan de Vries", "Transport",
			"Add me to SG-Transport, [department] needs it.",
		},
		{
			"department inside other identifiers kept", "it_transport, transport.local, Transport_Drivers, Transport.",
			"Jan de Vries", "Transport", "it_transport, transport.local, Transport_Drivers, [department].",
		},
		{
			"department beside a lone joiner", "Team - Transport - asked; also -Transport-.", "Jan de Vries", "Transport",
			"Team - [department] - asked; also -[department]-.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := redactRequester(c.summary, c.displayName, c.department); got != c.want {
				t.Errorf("redactRequester(%q) = %q, want %q", c.summary, got, c.want)
			}
		})
	}
}

// covers: spec 0009 AC-9. The rule runs after the collapse and before the
// escape, so a department with & still matches and the result is escaped.
func TestModelSummaryOrder(t *testing.T) {
	got := modelSummary("Someone\nin R&D\n\nasked <now>.", "Jan de Vries", "R&D")
	if want := "Someone in [department] asked &lt;now&gt;."; got != want {
		t.Errorf("modelSummary = %q, want %q", got, want)
	}
}
