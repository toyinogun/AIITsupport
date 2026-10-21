package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the zone data cmd/toolserver embeds

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
	"github.com/toyinogun/AIITsupport/internal/tools/toolstest"
	"github.com/toyinogun/AIITsupport/internal/zammad"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

const base = toolstest.Base

// --- the harness ---

type harness struct {
	t      *testing.T
	d      *Dispatcher
	store  *toolstest.MemStore
	dir    *toolstest.Directory
	zammad *zammadtest.Fake
	logs   *bytes.Buffer
	ks     *switchFile
	eva    int // eva.peters' Zammad user id
}

// switchFile is a kill switch file the test can rewrite.
type switchFile struct {
	t    *testing.T
	path string
}

func (s *switchFile) set(content string) {
	s.t.Helper()
	if err := os.WriteFile(s.path, []byte(content), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

func testConfig() Config {
	return Config{
		BaseDN: base, UserOUs: []string{"OU=Users,OU=Noord"}, GroupOUs: []string{"OU=Groups,OU=Noord"},
		DeniedGroups: []string{"SG-IT-Admins", "APP-Helpdesk-Agents", "SYNC-Entra-Users"},
		CallTimeout:  5 * time.Second, WriteLimit: 3, WriteWindow: time.Hour,
		TicketCreateLimit: 5, TicketMaxPending: 720 * time.Hour, PrivilegedWindow: 24 * time.Hour,
		ExecuteTimeout: 5 * time.Second, ExecuteGrace: 5 * time.Minute, AccessMaxPending: 3,
		KB: KBSettings{SearchLimit: 3, SearchCandidates: 10, StaleAfterDays: 730, BodyMaxRunes: 2000, Zone: amsterdam()},
	}
}

// amsterdam is the knowledge base's zone (the embedded zone data, as in
// cmd/toolserver).
func amsterdam() *time.Location {
	zone, err := LoadKBZone()
	if err != nil {
		panic(err)
	}
	return zone
}

func newHarness(t *testing.T, extra ...Tool) *harness {
	t.Helper()
	return newHarnessWith(t, nil, extra...)
}

// newHarnessWith is newHarness with the reset link system (spec 0011).
func newHarnessWith(t *testing.T, reset *ResetSystem, extra ...Tool) *harness {
	t.Helper()
	return newHarnessSystems(t, reset, nil, extra...)
}

// newHarnessSystems is newHarness with the optional reset and MFA systems.
func newHarnessSystems(t *testing.T, reset *ResetSystem, mfa *MFASystem, extra ...Tool) *harness {
	t.Helper()
	h := &harness{t: t, store: &toolstest.MemStore{}, dir: toolstest.NewDirectory(), zammad: zammadtest.New(t), logs: &bytes.Buffer{}}
	h.ks = &switchFile{t: t, path: filepath.Join(t.TempDir(), "kill-switch")}
	h.ks.set("read_write\n")
	h.eva = h.zammad.AddUser("eva.peters")
	h.zammad.AddUser("ruben.mulder")

	logger := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rec, err := audit.NewRecorder(h.store, audit.Config{OutcomeRetries: 0, WriteTimeout: time.Second, MaxPayloadBytes: 4096}, logger)
	if err != nil {
		t.Fatal(err)
	}
	client, err := zammad.New(zammad.Options{BaseURL: h.zammad.URL(), Auth: zammad.TokenAuth(zammadtest.Token), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if mfa != nil && mfa.Reader == nil {
		mfa.Reader = h.dir // the fake directory also answers by account name
	}
	reg, err := newRegistry(append(productionTools(), append([]Tool{groupSearchTool(), groupAccessRequestTool(8 * time.Hour)}, extra...)...))
	if err != nil {
		t.Fatal(err)
	}
	h.d, err = NewDispatcher(reg, testConfig(), Systems{
		Directory: h.dir, Groups: h.dir, Reader: h.dir, Writer: h.dir, Helpdesk: NewHelpdesk(client, "IT Support", "http://zammad.test/"),
		Recorder: rec, KillSwitch: NewKillSwitch(h.ks.path, true), Reset: reset, MFA: mfa,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	h.d.now = func() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) }
	return h
}

// eva is a requester whose email matches eva.peters.
func eva() Requester {
	return Requester{SlackID: "U01EVA", Email: "eva.peters@noord.example", ChannelID: "C01HELP", ThreadTS: "1760000000.000100"}
}

func (h *harness) call(tool string, args string, r Requester) Response {
	h.t.Helper()
	return h.d.Call(h.t.Context(), Request{Tool: tool, Args: []byte(args), Requester: r})
}

// pairs returns the outcome rows, which carry the intent's columns.
func (h *harness) outcomes() []queries.AuditLog {
	var out []queries.AuditLog
	for _, r := range h.store.All() {
		if r.Kind == "outcome" {
			out = append(out, r)
		}
	}
	return out
}

func (h *harness) intents() []queries.AuditLog {
	var out []queries.AuditLog
	for _, r := range h.store.All() {
		if r.Kind == "intent" {
			out = append(out, r)
		}
	}
	return out
}

func (h *harness) lastOutcome() queries.AuditLog {
	h.t.Helper()
	o := h.outcomes()
	if len(o) == 0 {
		h.t.Fatal("no outcome row")
	}
	return o[len(o)-1]
}

func (h *harness) lastIntent() queries.AuditLog {
	h.t.Helper()
	o := h.intents()
	if len(o) == 0 {
		h.t.Fatal("no intent row")
	}
	return o[len(o)-1]
}

func wantCode(t *testing.T, resp Response, code Code) {
	t.Helper()
	if resp.Code != code || !resp.IsError || resp.Data["code"] != string(code) || !strings.HasPrefix(resp.Text, string(code)+": ") {
		t.Fatalf("response = %+v, want a %s refusal", resp, code)
	}
}

func jsonOf(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	if raw == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return m
}

var errNetwork = goldap.NewError(goldap.ErrorNetwork, errors.New("connection refused"))
