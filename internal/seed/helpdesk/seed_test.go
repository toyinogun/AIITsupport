package helpdesk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/zammad"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

const testMarker = MarkerPrefix + "test"

// fakeRunner answers the Rails steps without Docker and records each call.
type fakeRunner struct {
	bootstrap func() (Output, error)
	backdate  func(stdin []byte) (Output, error)
	calls     []string
	envs      []map[string]string
	stdin     []byte
}

func (r *fakeRunner) Run(_ context.Context, step string, env map[string]string, stdin []byte) (Output, error) {
	r.calls = append(r.calls, step)
	r.envs = append(r.envs, env)
	switch step {
	case StepBootstrap:
		return r.bootstrap()
	case StepBackdate:
		r.stdin = stdin
		if r.backdate != nil {
			return r.backdate(stdin)
		}
		return Output{Stdout: []byte("I, Rails log line\nNOORD_RESULT {\"state\":\"seeded\"}\n")}, nil
	}
	return Output{}, errors.New("unknown step " + step)
}

func result(v any) func() (Output, error) {
	return func() (Output, error) {
		data, _ := json.Marshal(v)
		return Output{Stdout: append([]byte("I, a Rails log line\nNOORD_RESULT "), data...)}, nil
	}
}

// fixture builds a seeder against a fake Zammad holding every Noord user.
func fixture(t *testing.T) (Seeder, *fakeRunner, *zammadtest.Fake, map[string]syncedUser) {
	t.Helper()
	c, m := loadNoord(t)
	f := zammadtest.New(t)
	users := map[string]syncedUser{}
	for _, u := range m.Users {
		roles := []string{"Customer"}
		if u.Username == Agent {
			roles = []string{"Admin", "Agent"}
		}
		users[u.Username] = syncedUser{ID: f.AddUser(u.Username), Active: true, Organization: Organization, Roles: roles}
	}
	admin, err := zammad.New(zammad.Options{BaseURL: f.URL(), Timeout: 5 * time.Second, Auth: zammad.BasicAuth{Login: zammadtest.AdminLogin, Password: zammadtest.AdminPassword}})
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := zammad.New(zammad.Options{BaseURL: f.URL(), Timeout: 5 * time.Second, Auth: zammad.TokenAuth(zammadtest.Token)})
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	s := Seeder{
		Runner: r, Admin: admin, Assistant: assistant, Content: c, Model: m, Marker: testMarker, Group: "IT Support",
		Secrets: Secrets{AdminEmail: zammadtest.AdminLogin, AdminPassword: "admin-secret-value", BindPassword: "bind-secret-value", AssistantToken: zammadtest.Token},
		Now:     func() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) },
	}
	return s, r, f, users
}

func freshResult(users map[string]syncedUser) map[string]any {
	return map[string]any{"state": "fresh", "kb_id": 1, "kb_locale_id": 1, "group_id": 1, "users": users}
}

func writes(f *zammadtest.Fake) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Method != http.MethodGet {
			n++
		}
	}
	return n
}

// covers: AC-8, AC-9, AC-16. Bootstrap, then the REST fill, then backdate
// with the marker; tickets are authored with From, then closed by Ruben.
func TestSeedFreshOrder(t *testing.T) {
	s, r, f, users := fixture(t)
	r.bootstrap = result(freshResult(users))

	res, err := s.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Join(r.calls, ",") != "bootstrap,backdate" {
		t.Errorf("steps = %v", r.calls)
	}
	if res.Tickets != 20 || res.Answers != 15 || res.Categories != 6 || res.Articles < 20 {
		t.Errorf("result = %+v", res)
	}

	var p backdatePayload
	if err := json.Unmarshal(r.stdin, &p); err != nil {
		t.Fatalf("backdate stdin: %v", err)
	}
	if p.Marker != testMarker || r.envs[1]["NOORD_MARKER"] != testMarker {
		t.Errorf("backdate marker = %q, env %q", p.Marker, r.envs[1]["NOORD_MARKER"])
	}
	if len(p.Tickets) != 20 || len(p.Answers) != 15 {
		t.Fatalf("payload has %d tickets, %d answers", len(p.Tickets), len(p.Answers))
	}
	first := p.Tickets[0]
	if first.Key != "hd-001" || first.CreatedAt.Format(time.RFC3339) != "2026-08-03T06:12:00Z" ||
		first.CloseAt == nil || first.FirstResponseAt == nil || first.FirstResponseAt.Format(time.RFC3339) != "2026-08-03T06:31:00Z" {
		t.Errorf("hd-001 dates = %+v", first)
	}

	var creates, closes, pendings, opens int
	for _, req := range f.Requests() {
		switch {
		case req.Method == http.MethodPost && req.Path == "/api/v1/tickets":
			creates++
			if req.Body["customer_id"] == nil || req.From == "" || req.From == Agent {
				t.Errorf("ticket create %+v, want From the customer and a numeric customer_id", req)
			}
		case req.Method == http.MethodPut:
			if req.From != Agent {
				t.Errorf("state update From = %q, want %s", req.From, Agent)
			}
			switch req.Body["state"] {
			case StateClosed:
				closes++
			case StateOpen:
				opens++
				if _, ok := req.Body["owner_id"]; ok {
					t.Errorf("open ticket got an owner: %+v", req.Body)
				}
			case zammadPending:
				pendings++
				if req.Body["pending_time"] != "2026-11-17T12:00:00Z" {
					t.Errorf("pending_time = %v, want run time plus 30 days", req.Body["pending_time"])
				}
			}
		case req.Method != http.MethodGet && zammad.Guarded(req.Path):
			t.Errorf("user write %s %s", req.Method, req.Path)
		}
	}
	if creates != 20 || closes != 17 || pendings != 1 || opens != 2 {
		t.Errorf("creates=%d closes=%d pendings=%d opens=%d, want 20, 17, 1, 2", creates, closes, pendings, opens)
	}
}

// covers: AC-9. already_seeded makes no writes; a changed token asks for a reset.
func TestSeedAlreadySeeded(t *testing.T) {
	s, r, f, _ := fixture(t)
	r.bootstrap = result(map[string]any{"state": "already_seeded"})
	res, err := s.Run(t.Context())
	if err != nil || !res.AlreadySeeded {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	if writes(f) != 0 || len(r.calls) != 1 {
		t.Errorf("writes=%d steps=%v, want none after bootstrap", writes(f), r.calls)
	}

	other, err := zammad.New(zammad.Options{BaseURL: f.URL(), Timeout: 5 * time.Second, Auth: zammad.TokenAuth("a-token-that-changed-since-the-seed-000")})
	if err != nil {
		t.Fatal(err)
	}
	s.Assistant = other
	if _, err := s.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "make helpdesk-reset") {
		t.Errorf("changed token: err = %v", err)
	}
}

// covers: AC-9. needs_reset stops before any write.
func TestSeedNeedsReset(t *testing.T) {
	s, r, f, _ := fixture(t)
	r.bootstrap = result(map[string]any{"state": "needs_reset"})
	if _, err := s.Run(t.Context()); !errors.Is(err, ErrNeedsReset) {
		t.Fatalf("err = %v, want ErrNeedsReset", err)
	}
	if writes(f) != 0 {
		t.Errorf("%d writes", writes(f))
	}
}

// covers: AC-14. A sync that does not match the users: list fails, listing
// every problem, before any ticket or KB write.
func TestSeedUserCheck(t *testing.T) {
	s, r, f, users := fixture(t)
	delete(users, "eva.peters")
	users["kevin.smits"] = syncedUser{ID: users["kevin.smits"].ID, Active: false, Organization: Organization, Roles: []string{"Customer"}}
	users[Agent] = syncedUser{ID: users[Agent].ID, Active: true, Organization: Organization, Roles: []string{"Customer"}}
	users["adm.ruben.mulder"] = syncedUser{ID: 9999, Active: true, Organization: Organization, Roles: []string{"Customer"}}
	r.bootstrap = result(freshResult(users))

	_, err := s.Run(t.Context())
	if err == nil {
		t.Fatal("Run accepted a mismatched sync")
	}
	for _, want := range []string{"eva.peters is missing", "kevin.smits is inactive", "ruben.mulder has roles [Customer]", "adm.ruben.mulder was synced"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if writes(f) != 0 || len(r.calls) != 1 {
		t.Errorf("writes=%d steps=%v", writes(f), r.calls)
	}
}

// covers: AC-14, AC-15. A failed or stopped step shows its output without
// any secret value and says to reset.
func TestSeedStepFailureRedacts(t *testing.T) {
	s, r, _, _ := fixture(t)
	r.bootstrap = func() (Output, error) {
		return Output{Stderr: []byte("NOORD_FAILED seed admin: password admin-secret-value rejected, bind bind-secret-value\n")}, ErrStepTimeout
	}
	_, err := s.Run(t.Context())
	if !errors.Is(err, ErrStepTimeout) {
		t.Fatalf("err = %v", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "admin-secret-value") || strings.Contains(msg, "bind-secret-value") {
		t.Errorf("error leaks a secret: %s", msg)
	}
	if !strings.Contains(msg, "seed admin") || !strings.Contains(msg, "make helpdesk-reset") {
		t.Errorf("error = %s", msg)
	}
}

func TestSeedLDAPFailureNamesSamba(t *testing.T) {
	s, r, _, _ := fixture(t)
	r.bootstrap = func() (Output, error) {
		return Output{Stderr: []byte("NOORD_FAILED ldap import: RuntimeError: import error: Connection refused\n")}, errors.New("exit status 1")
	}
	if _, err := s.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "ldaps://samba:636") {
		t.Errorf("err = %v", err)
	}
}

func TestComposeRunnerRefusesBadInput(t *testing.T) {
	r := ComposeRunner{Limit: time.Second}
	if _, err := r.Run(t.Context(), "../evil", nil, nil); err == nil {
		t.Error("accepted a step name with a path")
	}
	if _, err := r.Run(t.Context(), "bootstrap", map[string]string{"a=b": "x"}, nil); err == nil {
		t.Error("accepted a bad environment name")
	}
}

// covers: AC-9. The marker covers every helpdesk file (not dotfiles), the
// user list, and the CA, in a fixed order.
func TestMarker(t *testing.T) {
	dir := t.TempDir()
	hd := filepath.Join(dir, "helpdesk")
	mustWrite := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(hd, "kb.yaml"), "a")
	mustWrite(filepath.Join(hd, "kb", "x.md"), "b")
	users := filepath.Join(dir, "noord.yaml")
	ca := filepath.Join(dir, "ca.pem")
	mustWrite(users, "u")
	mustWrite(ca, "c")

	m1, err := Marker(dir, hd, users, ca)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(m1, MarkerPrefix) || len(m1) != len(MarkerPrefix)+64 {
		t.Errorf("marker = %q", m1)
	}
	mustWrite(filepath.Join(hd, ".DS_Store"), "junk")
	if m2, _ := Marker(dir, hd, ca, users); m2 != m1 {
		t.Error("a dotfile or the argument order changed the marker")
	}
	mustWrite(ca, "new cert")
	m3, _ := Marker(dir, hd, users, ca)
	if m3 == m1 {
		t.Error("a new CA did not change the marker")
	}
	t.Chdir(hd)
	if m4, _ := Marker("..", ".", "../noord.yaml", "../ca.pem"); m4 != m3 {
		t.Error("the marker depends on the working directory")
	}
	if _, err := Marker(hd, ".", "../ca.pem"); err == nil {
		t.Error("accepted a file outside the root")
	}
}

func TestToHTMLEscapesRawHTML(t *testing.T) {
	html, err := ToHTML("**bold** <script>alert(1)</script>")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "<strong>bold</strong>") || strings.Contains(html, "<script>") {
		t.Errorf("html = %q", html)
	}
}

// covers: AC-14, AC-15. A step that dies without a NOORD_FAILED line (a Ruby
// crash with a long trace, say) names the step, keeps only the tail of its
// output in the error, and leaks no secret.
func TestSeedStepCrashKeepsTheTail(t *testing.T) {
	s, r, _, _ := fixture(t)
	trace := strings.Repeat("from /opt/zammad/lib/noise.rb:1:in `block'\n", 200)
	r.bootstrap = func() (Output, error) {
		return Output{
			Stderr: []byte("HEAD-OF-TRACE password admin-secret-value\n" + trace + "RuntimeError: TAIL-OF-TRACE\n"),
		}, errors.New("exit status 1")
	}
	_, err := s.Run(t.Context())
	if err == nil {
		t.Fatal("Run accepted a crashed bootstrap")
	}
	msg := err.Error()
	if !strings.Contains(msg, "TAIL-OF-TRACE") || strings.Contains(msg, "HEAD-OF-TRACE") {
		t.Errorf("error should keep the tail and drop the head: %.300s", msg)
	}
	if strings.Contains(msg, "admin-secret-value") {
		t.Error("error leaks a secret")
	}
	if !strings.Contains(msg, "rails step bootstrap failed") {
		t.Errorf("error does not name the step: %.300s", msg)
	}
	if len(msg) > 2*maxOutput {
		t.Errorf("error is %d bytes, want the output capped near %d", len(msg), maxOutput)
	}
}
