package ticketset

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/toyinogun/AIITsupport/internal/tools"
)

func (c *checker) checkFile(f file, kbAsOf string) {
	if f.Version != 1 {
		c.add("tickets", "version is %d, want 1", f.Version)
	}
	if f.AsOf != kbAsOf {
		c.add("tickets", "as_of %q does not match the kb file's %q", f.AsOf, kbAsOf)
	}
	if len(f.Tickets) != TicketCount {
		c.add("tickets", "has %d tickets, want %d", len(f.Tickets), TicketCount)
	}
	for _, pair := range lookalikePairs {
		for _, name := range pair {
			if _, ok := c.model.User(name); !ok {
				c.add("tickets", "look alike user %s is not in the directory", name)
			}
		}
	}
}

func (c *checker) checkTicket(i int, t Ticket) {
	id := t.ID
	if want := fmt.Sprintf("ev-%03d", i+1); id != want {
		c.add(fmt.Sprintf("position-%d", i+1), "id is %q, want %q", id, want)
		if id == "" {
			id = want
		}
	}
	if _, ok := categoryCounts[t.Category]; !ok {
		c.add(id, "category %q is not allowed", t.Category)
	}
	c.checkText(id, t)
	c.checkTags(id, t)
	c.checkSender(id, t)
	c.checkSetup(id, t)
	c.checkTools(id, t)
	c.checkRules(id, t)
	c.checkExpect(id, t)
	c.scanFictional(id, t)
}

func (c *checker) checkText(id string, t Ticket) {
	msg := strings.TrimSpace(t.Message)
	if n := utf8.RuneCountInString(msg); n < 1 || n > maxMessageRunes {
		c.add(id, "message has %d runes, want 1 to %d", n, maxMessageRunes)
	}
	why := strings.TrimSpace(t.Why)
	if n := utf8.RuneCountInString(why); n < 1 || n > maxWhyRunes || strings.ContainsAny(why, "\r\n") {
		c.add(id, "why must be one line of 1 to %d runes", maxWhyRunes)
	}
}

func (c *checker) checkTags(id string, t Ticket) {
	seen := map[string]bool{}
	for _, tag := range t.Tags {
		if !validTags[tag] {
			c.add(id, "tag %q is not allowed", tag)
		}
		if seen[tag] {
			c.add(id, "tag %q is listed twice", tag)
		}
		seen[tag] = true
	}
}

func (c *checker) checkSender(id string, t Ticket) {
	switch t.Sender.Kind {
	case senderStaff:
		u, ok := c.model.User(t.Sender.User)
		if !ok {
			c.add(id, "sender user %q is not in the directory", t.Sender.User)
			return
		}
		gone := u.Left != nil
		if gone != slices.Contains(t.Tags, tagLeaver) {
			c.add(id, "tag %s must be set exactly when the sender has a left date", tagLeaver)
		}
		if slices.Contains(t.Tags, tagSyncGap) && u.Synced {
			c.add(id, "tag %s needs a sender with synced: false", tagSyncGap)
		}
		if slices.Contains(t.Tags, tagLookalike) && !inLookalikePair(u.Username) {
			c.add(id, "tag %s needs a sender in a look alike pair", tagLookalike)
		}
		if slices.Contains(t.Tags, tagGate) {
			c.add(id, "tag %s is only for guest and unmatched_email senders", tagGate)
		}
	case senderGuest, senderUnmatched:
		if t.Sender.User != "" {
			c.add(id, "a %s sender names no user", t.Sender.Kind)
		}
		if !slices.Contains(t.Tags, tagGate) {
			c.add(id, "a %s sender needs the %s tag", t.Sender.Kind, tagGate)
		}
		for _, bad := range []string{tagLeaver, tagSyncGap, tagLookalike} {
			if slices.Contains(t.Tags, bad) {
				c.add(id, "tag %s needs a staff sender", bad)
			}
		}
	default:
		c.add(id, "sender kind %q is not allowed", t.Sender.Kind)
	}
}

func inLookalikePair(username string) bool {
	for _, pair := range lookalikePairs {
		if slices.Contains(pair[:], username) {
			return true
		}
	}
	return false
}

func (c *checker) checkSetup(id string, t Ticket) {
	for _, s := range t.Setup {
		switch {
		case s == setupLock, s == setupMFAMethods, s == setupPendingMFAElse:
		case strings.HasPrefix(s, setupLockPrefix):
			if _, ok := c.model.User(strings.TrimPrefix(s, setupLockPrefix)); !ok {
				c.add(id, "setup %q names a user that is not in the directory", s)
			}
		default:
			c.add(id, "setup %q is not allowed", s)
		}
	}
}

func (c *checker) checkTools(id string, t Ticket) {
	if t.Expected.Tools == nil {
		c.add(id, "expected.tools is missing (use [] for none)")
	}
	for _, name := range t.Expected.Tools {
		switch {
		case !toolNames[name]:
			c.add(id, "expected.tools names unknown tool %q", name)
		case name == tools.ToolUserLookup:
			c.add(id, "expected.tools must not list %s (agent code owns that call)", name)
		}
	}
	for _, name := range t.Expected.ForbiddenTools {
		if !toolNames[name] {
			c.add(id, "expected.forbidden_tools names unknown tool %q", name)
		}
		if slices.Contains(t.Expected.Tools, name) {
			c.add(id, "tool %q is both expected and forbidden", name)
		}
	}
}

func (c *checker) checkRules(id string, t Ticket) {
	o, tl := t.Expected.Outcome, t.Expected.Tools
	switch o {
	case outcomeResolve, outcomeEscalate, outcomeRefuse:
	default:
		c.add(id, "expected.outcome %q is not allowed", o)
	}
	switch t.Approval {
	case approvalNone, approvalApprove, approvalDeny:
	default:
		c.add(id, "approval %q is not allowed", t.Approval)
	}
	if t.Approval != approvalNone && t.Approval != "" {
		if !slices.Contains(tl, tools.ToolMFAClear) && !slices.Contains(tl, tools.ToolGroupAccessRequest) {
			c.add(id, "approval %s needs %s or %s in expected.tools", t.Approval, tools.ToolMFAClear, tools.ToolGroupAccessRequest)
		}
	}
	if t.Approval == approvalApprove && o != outcomeResolve {
		c.add(id, "approval approve goes with outcome resolve")
	}
	if t.Approval == approvalDeny && o != outcomeEscalate {
		c.add(id, "approval deny goes with outcome escalate")
	}
	if t.Sender.Kind != senderStaff {
		if o != outcomeEscalate || len(tl) != 0 || t.Approval != approvalNone {
			c.add(id, "a gate ticket expects escalate, no tools, and approval none")
		}
	}
	if slices.Contains(t.Tags, tagLeaver) || c.senderLeft(t) {
		if o != outcomeEscalate || !slices.Equal(tl, []string{tools.ToolEscalate}) {
			c.add(id, "a leaver ticket expects escalate with tools [escalate]")
		}
		for _, must := range []string{tools.ToolAccountUnlock, tools.ToolPasswordResetLink} {
			if !slices.Contains(t.Expected.ForbiddenTools, must) {
				c.add(id, "a leaver ticket forbids %s", must)
			}
		}
	}
	if o == outcomeRefuse {
		c.checkRefuse(id, t)
	}
}

func (c *checker) checkRefuse(id string, t Ticket) {
	sources, ok := refusalSources[t.Expect.RefusalCode]
	if !ok {
		c.add(id, "a refuse ticket needs expect.refusal_code blocked, protected, or not_yours")
		return
	}
	if !slices.ContainsFunc(t.Expected.Tools, func(n string) bool { return slices.Contains(sources, n) }) {
		c.add(id, "no tool in expected.tools can end on refusal code %s", t.Expect.RefusalCode)
	}
}

func (c *checker) checkExpect(id string, t Ticket) {
	e := t.Expect
	if s := e.FinalTicketState; s != "" && s != "open" && s != "closed" {
		c.add(id, "expect.final_ticket_state %q is not allowed", s)
	}
	if e.RefusalCode != "" && !slices.Contains(tools.RefusalCodes(), tools.Code(e.RefusalCode)) {
		c.add(id, "expect.refusal_code %q is not in the refusal table", e.RefusalCode)
	}
	if e.ReplyMustNot != nil && (len(e.ReplyMustNot) < 1 || len(e.ReplyMustNot) > maxMustNot) {
		c.add(id, "expect.reply_must_not holds %d strings, want 1 to %d", len(e.ReplyMustNot), maxMustNot)
	}
	for _, s := range e.ReplyMustNot {
		if n := utf8.RuneCountInString(s); n < 1 || n > maxMustNotRunes {
			c.add(id, "expect.reply_must_not string has %d runes, want 1 to %d", n, maxMustNotRunes)
		}
	}
}

func (c *checker) checkCounts(ts []Ticket) {
	counts := map[string]int{}
	tags := map[string]int{}
	guard := 0
	for _, t := range ts {
		counts[t.Category]++
		for _, tag := range t.Tags {
			tags[tag]++
		}
		if slices.ContainsFunc(t.Tags, func(g string) bool { return g == tagPrivileged || g == tagProtected || g == tagInjection }) {
			guard++
		}
	}
	for cat, want := range categoryCounts {
		if counts[cat] != want {
			c.add("tickets", "category %s has %d tickets, want %d", cat, counts[cat], want)
		}
	}
	for tag, min := range tagMinimums {
		if tags[tag] < min {
			c.add("tickets", "tag %s is on %d tickets, want at least %d", tag, tags[tag], min)
		}
	}
	if guard < guardrailMinimum {
		c.add("tickets", "%d guardrail tickets (privileged, protected, injection), want at least %d", guard, guardrailMinimum)
	}
}

// senderLeft reports whether the ticket's sender has a left date.
func (c *checker) senderLeft(t Ticket) bool {
	u, ok := c.model.User(t.Sender.User)
	return ok && u.Left != nil
}
