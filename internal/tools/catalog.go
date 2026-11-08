package tools

import (
	"fmt"
	"strings"

	"github.com/toyinogun/AIITsupport/internal/audit"
)

// catalogHeader opens the generated handover tool spec (feature #22).
const catalogHeader = `# Tools and risk tiers

_Generated from the tool registry by ` + "`Catalog`" + ` in ` + "`internal/tools/catalog.go`" + `. Do not edit by hand:
` + "`TestCatalogMatchesHandoverDoc`" + ` fails when this file and the code disagree. After a registry or
refusal change, run ` + "`go test ./internal/tools/ -run TestCatalogMatchesHandoverDoc -update`" + `._

This is every tool the tool server can serve, with its risk tier, what guards it, and what the audit
log keeps. How the pieces fit is in [architecture.md](architecture.md); how to add a tool is in
[runbook.md](runbook.md#add-a-tool).

## Risk tiers

| Tier | Meaning |
|---|---|
| read | Reads only, or writes to this conversation's own ticket. Runs right away. |
| low | A small change to the requester's own account or ticket. Runs right away, inside its guards. |
| medium | A change a human must approve in Slack first. The tool only files the request; the change runs after the click. |
| blocked | Never runs. The call is refused and audited, and the agent escalates. |

Every call, whatever its tier, passes the same dispatcher steps and writes an intent row to ` + "`audit_log`" + `
before it runs and an outcome row after. If the intent row cannot be written, nothing runs. If the
outcome row fails after the action ran, only the intent row is left.
`

// optionalTools says when a tool outside the always on set is registered.
var optionalTools = map[string]string{
	ToolPasswordResetLink: "only when the `RESET_*` settings are set",
	ToolMFAClear:          "only when the `GRAPH_*` settings and `SLACK_IT_APPROVERS` are set",
}

// Catalog renders the registry and the refusal table as Markdown. The output
// is deterministic: registry order, then refusal table order.
func Catalog(r *Registry) string {
	var b strings.Builder
	b.WriteString(catalogHeader)
	writeSummary(&b, r.Tools())
	for _, t := range r.Tools() {
		writeTool(&b, t)
	}
	writeRefusals(&b)
	return b.String()
}

func writeSummary(b *strings.Builder, tools []Tool) {
	b.WriteString("\n## Summary\n\n| Tool | Tier | What it does | Guards beyond identity and schema | Registered |\n|---|---|---|---|---|\n")
	for _, t := range tools {
		fmt.Fprintf(b, "| `%s` | %s | %s | %s | %s |\n", t.Name, t.Tier, effect(t), guards(t), registered(t.Name))
	}
}

func writeTool(b *strings.Builder, t Tool) {
	fmt.Fprintf(b, "\n### `%s`\n\n**Tier:** %s · **Acts on:** %s\n\n", t.Name, t.Tier, targetText(t.Target))
	fmt.Fprintf(b, "What the model reads: %s\n", t.Description)
	if len(t.Fields) == 0 {
		b.WriteString("\nTakes no arguments.\n")
	} else {
		b.WriteString("\n| Argument | Required | Rule | Kept in the audit log |\n|---|---|---|---|\n")
		for _, f := range t.Fields {
			fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", f.Name, yesNo(f.Required), ruleText(f), keptText(t, f))
		}
	}
	if len(t.StoreResult) > 0 {
		fmt.Fprintf(b, "\nResult keys kept in the audit log: %s.\n", codeList(t.StoreResult))
	}
	if len(t.PendingResult) > 0 {
		fmt.Fprintf(b, "\nKept while the approval is pending: %s.\n", codeList(t.PendingResult))
	}
}

func writeRefusals(b *strings.Builder) {
	b.WriteString("\n## Refusals\n\n" +
		"A refusal is a safe stop, not an error. The model sees only the code and the fixed sentence; the audit\n" +
		"log stores the outcome and the fixed reason, never request data. Failures (`unavailable`, `failed`,\n" +
		"`not_confirmed`, `invalid_arguments`) are not refusals and are not listed here.\n\n" +
		"| Code | Audit outcome | Stored reason | What the model is told |\n|---|---|---|---|\n")
	for _, r := range allRefusals {
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", r.code, r.outcome, r.reason, r.sentence)
	}
}

// effect says in plain words what the handler kind can touch.
func effect(t Tool) string {
	switch t.Handler.(type) {
	case nil:
		return "nothing, it never runs"
	case AccountHandler:
		return "changes the requester's own directory account"
	case ResetHandler:
		return "issues a single use password reset link"
	case MFAHandler:
		return "clears the requester's Entra MFA methods"
	case GroupHandler:
		return "adds the requester to a directory group"
	default:
		return "reads, or writes this conversation's ticket"
	}
}

// guards mirrors the dispatcher: blocked first, then approvals, then the
// kill switch and limits from accountChange and TicketLimited (limitFor).
func guards(t Tool) string {
	if t.Tier == audit.TierBlocked {
		return "always refused"
	}
	var g []string
	switch t.Handler.(type) {
	case MFAHandler:
		g = append(g, "IT approval in Slack")
	case GroupHandler:
		g = append(g, "group owner approval in Slack")
	}
	switch {
	case t.accountChange():
		g = append(g, "kill switch, `TOOL_WRITE_LIMIT`")
	case t.TicketLimited:
		g = append(g, "`TICKET_CREATE_LIMIT`")
	}
	if len(g) == 0 {
		return "none"
	}
	return strings.Join(g, ", ")
}

func registered(name string) string {
	if when, ok := optionalTools[name]; ok {
		return when
	}
	return "always"
}

func targetText(t Target) string {
	switch t {
	case TargetRequester:
		return "the requester's own account"
	case TargetTicket:
		return "this conversation's ticket"
	case TargetGroup:
		return "one group, once the precheck resolved it"
	default:
		return "nothing named"
	}
}

// ruleText says what the argument validator accepts for one field.
func ruleText(f Field) string {
	switch {
	case f.Bool:
		return "true or false"
	case len(f.Enum) > 0:
		return "one of " + codeList(f.Enum)
	case f.List:
		return fmt.Sprintf("a list of %d to %d items, each %s", f.MinItems, f.MaxItems, stringRule(f))
	default:
		return stringRule(f)
	}
}

func stringRule(f Field) string {
	var parts []string
	if f.NoNewline {
		parts = append(parts, "one line")
	}
	switch {
	case f.MaxRunes > 0:
		parts = append(parts, fmt.Sprintf("%d to %d characters", f.MinRunes, f.MaxRunes))
	case f.MinRunes > 0:
		parts = append(parts, fmt.Sprintf("at least %d characters", f.MinRunes))
	}
	if len(parts) == 0 {
		return "any text"
	}
	return strings.Join(parts, ", ")
}

func keptText(t Tool, f Field) string {
	for _, key := range t.Store {
		if key == f.Name {
			return "yes"
		}
	}
	if f.FreeText {
		return "no (free text)"
	}
	return "no"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func codeList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = "`" + s + "`"
	}
	return strings.Join(quoted, ", ")
}
