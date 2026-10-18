package tools

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/audit"
)

// NewTestRegistry is the production registry plus extra test only tools. It
// lives in a _test.go file, so no binary can reach it.
func NewTestRegistry(extra ...Tool) (*Registry, error) {
	return newRegistry(append(productionTools(), extra...))
}

// countingTool is a test tool whose handler counts its runs.
func countingTool(name string, tier audit.Tier, account bool, runs *atomic.Int32) Tool {
	t := Tool{Name: name, Description: "test only", Tier: tier, Target: TargetRequester}
	if account {
		t.Handler = AccountHandler(func(context.Context, AccountDeps, Input) (Result, error) {
			runs.Add(1)
			return Result{Result: resultData(map[string]any{}), Text: "ran"}, nil
		})
	} else {
		t.Handler = ReadHandler(func(context.Context, Deps, Input) (Result, error) {
			runs.Add(1)
			return Result{Result: resultData(map[string]any{}), Text: "ran"}, nil
		})
	}
	return t
}

// covers: AC-1, AC-14. The production registry lists exactly the five
// tools, none of them a test tool, and nothing that approves.
func TestProductionRegistry(t *testing.T) {
	reg, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range reg.Tools() {
		names = append(names, tool.Name)
		schema := tool.InputSchema()
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%s schema = %v, want a closed object", tool.Name, schema)
		}
		if strings.Contains(strings.ToLower(tool.Name+tool.Description), "approve") {
			t.Errorf("%s mentions approving", tool.Name)
		}
	}
	want := []string{ToolUserLookup, ToolAccountUnlock, ToolTicketCreate, ToolTicketUpdate, ToolPrivilegedAccessChange}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
	if tool, _ := reg.Lookup(ToolTicketCreate); !slices.Equal(tool.InputSchema()["required"].([]string), []string{"title", "body"}) {
		t.Errorf("ticket_create required = %v", tool.InputSchema()["required"])
	}
}

// covers: AC-21. A tool is an account change exactly when its handler takes
// AccountDeps; the production set has one, account_unlock.
func TestAccountChangeFollowsTheHandlerType(t *testing.T) {
	reg, _ := NewRegistry()
	for _, tool := range reg.Tools() {
		if tool.accountChange() != (tool.Name == ToolAccountUnlock) {
			t.Errorf("%s accountChange = %v", tool.Name, tool.accountChange())
		}
	}
}

// covers: AC-21, AC-12. Registry construction refuses a medium tool with no
// Approval hook, a blocked tool with a handler, and stored free text.
func TestRegistryConstructionRules(t *testing.T) {
	var runs atomic.Int32
	medium := countingTool("t_medium", audit.TierMedium, false, &runs)
	blocked := Tool{Name: "t_blocked", Description: "x", Tier: audit.TierBlocked, Handler: medium.Handler}
	freeText := countingTool("t_free", audit.TierLow, false, &runs)
	freeText.Fields = []Field{{Name: "note", MinRunes: 1, FreeText: true}}
	freeText.Store = []string{"note"}
	noHandler := Tool{Name: "t_none", Description: "x", Tier: audit.TierLow}
	dup := countingTool(ToolUserLookup, audit.TierRead, false, &runs)

	for name, tool := range map[string]Tool{
		"medium without hook": medium, "blocked with handler": blocked,
		"stored free text": freeText, "no handler": noHandler, "duplicate name": dup,
	} {
		if _, err := NewTestRegistry(tool); err == nil {
			t.Errorf("%s: registry built, want an error", name)
		}
	}
}

// covers: AC-14. A medium tool is deferred with its hook's request; its
// handler never runs, and the model is told it waits for IT.
func TestMediumToolIsDeferred(t *testing.T) {
	var runs atomic.Int32
	tool := countingTool("t_medium", audit.TierMedium, false, &runs)
	expires := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	tool.Approval = func(_ context.Context, c audit.Call) (audit.ApprovalRequest, error) {
		if c.RequesterAccount != "eva.peters" {
			t.Errorf("hook saw account %q", c.RequesterAccount)
		}
		return audit.ApprovalRequest{Kind: audit.ApproverIT, Approvers: []string{"U09IT"}, ExpiresAt: expires}, nil
	}
	h := newHarness(t, tool)

	resp := h.call("t_medium", "", eva())
	if resp.IsError || resp.Code != CodePendingApproval || resp.Text != "This request is waiting for IT approval." ||
		resp.Data["code"] != "pending_approval" {
		t.Fatalf("response = %+v", resp)
	}
	if runs.Load() != 0 {
		t.Fatal("a medium tool's handler ran")
	}
	if len(h.store.Approvals) != 1 || h.store.Approvals[0].ToolName != "t_medium" || !h.store.Approvals[0].ExpiresAt.Equal(expires) {
		t.Errorf("approvals = %+v", h.store.Approvals)
	}
	if o := h.lastOutcome(); o.Outcome.String != "pending_approval" || !o.ApprovalID.Valid {
		t.Errorf("outcome = %+v", o)
	}

	// A failing hook is an error, not a pending approval.
	tool.Approval = func(context.Context, audit.Call) (audit.ApprovalRequest, error) {
		return audit.ApprovalRequest{}, errors.New("no approvers configured")
	}
	h = newHarness(t, tool)
	wantCode(t, h.call("t_medium", "", eva()), CodeFailed)
	if o := h.lastOutcome(); o.Outcome.String != "error" {
		t.Errorf("outcome = %s", o.Outcome.String)
	}
}

// covers: AC-8, AC-21. The dispatcher's own kill switch read stops an
// AccountHandler before it runs; a ReadHandler is never stopped.
func TestDispatcherGatesEveryAccountHandler(t *testing.T) {
	var accountRuns, readRuns atomic.Int32
	h := newHarness(t,
		countingTool("t_account", audit.TierLow, true, &accountRuns),
		countingTool("t_read", audit.TierLow, false, &readRuns))
	h.ks.set("read_only")

	wantCode(t, h.call("t_account", "", eva()), CodeReadOnly)
	if accountRuns.Load() != 0 {
		t.Fatal("an account handler ran while account changes were paused")
	}
	if resp := h.call("t_read", "", eva()); resp.IsError || readRuns.Load() != 1 {
		t.Fatalf("read handler: %+v, runs %d", resp, readRuns.Load())
	}

	// And every AccountHandler is rate limited, with no flag to set.
	h.ks.set("read_write")
	for range 3 {
		h.call("t_account", "", eva())
	}
	wantCode(t, h.call("t_account", "", eva()), CodeRateLimited)
	if accountRuns.Load() != 3 {
		t.Errorf("account handler ran %d times, want 3", accountRuns.Load())
	}
}

// covers: AC-16. A bad OU list stops the tool server at startup.
func TestDispatcherRefusesBadOUs(t *testing.T) {
	for name, ous := range map[string][]string{
		"empty list":  nil,
		"empty entry": {"OU=Users,OU=Noord", ""},
		"DC= entry":   {"OU=Users,OU=Noord,DC=ad,DC=noord,DC=example"},
		"not a DN":    {"Users"},
	} {
		cfg := testConfig()
		cfg.UserOUs = ous
		h := newHarness(t)
		if _, err := NewDispatcher(h.d.reg, cfg, h.d.sys, h.d.logger); err == nil {
			t.Errorf("%s: dispatcher built, want an error", name)
		}
	}
}

// covers: AC-13. The fence refuses the floor, the configured groups,
// missing, outside, and nested groups, and allows an ordinary group.
func TestFenceCheckGroup(t *testing.T) {
	h := newHarness(t)
	f := h.d.Fence()
	for _, name := range []string{
		"Domain Admins", "domain admins", "SG-IT-Admins", "APP-Helpdesk-Agents",
		"SYNC-Entra-Users", "No-Such-Group", "T-Nested", "Outside-Group", "Enterprise Admins",
	} {
		if err := f.CheckGroup(t.Context(), name); !errors.Is(err, ErrGroupProtected) {
			t.Errorf("CheckGroup(%s) = %v, want ErrGroupProtected", name, err)
		}
	}
	if err := f.CheckGroup(t.Context(), "SG-Finance"); err != nil {
		t.Errorf("CheckGroup(SG-Finance) = %v, want allowed", err)
	}

	// A configured name that does not resolve fails every check closed.
	delete(h.dir.Groups, "SYNC-Entra-Users")
	if err := f.CheckGroup(t.Context(), "SG-Finance"); !errors.Is(err, ErrGroupProtected) {
		t.Errorf("with an unresolved denied group, CheckGroup(SG-Finance) = %v, want ErrGroupProtected", err)
	}
	if !strings.Contains(h.logs.String(), "denied_group_unresolved") {
		t.Error("no denied_group_unresolved log")
	}

	// The directory down is classified, not reported as protected.
	h.dir.Err = errNetwork
	if err := f.CheckGroup(t.Context(), "SG-Finance"); err == nil || errors.Is(err, ErrGroupProtected) {
		t.Errorf("CheckGroup with AD down = %v, want a directory failure", err)
	} else if asFailure(err).code != CodeUnavailable {
		t.Errorf("code = %s, want unavailable", asFailure(err).code)
	}
}
