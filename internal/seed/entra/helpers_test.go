package entra

import (
	"bytes"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/graph/graphtest"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

const (
	noordSeed  = "../../../seed/directory/noord.yaml"
	testBase   = "DC=ad,DC=noord,DC=example"
	testDomain = "noordtest.onmicrosoft.com"
)

func loadNoord(t *testing.T) directory.Model {
	t.Helper()
	m, err := directory.Load(noordSeed, "")
	if err != nil {
		t.Fatalf("load noord.yaml: %v", err)
	}
	return m
}

// scopeOf is the AD scope a freshly seeded directory holds: every synced
// user, enabled (leavers stay enabled), with its seed file manager.
func scopeOf(m directory.Model) []ADUser {
	var out []ADUser
	for _, u := range m.Users {
		if !u.Synced {
			continue
		}
		dn, _ := m.UserDN(testBase, u.Username)
		out = append(out, ADUser{
			DN: dn, Username: u.Username, DisplayName: u.DisplayName(), GivenName: u.GivenName,
			Surname: u.Surname, Department: u.Department, Title: u.Title, Manager: u.Manager,
		})
	}
	return out
}

func without(scope []ADUser, username string) []ADUser {
	var out []ADUser
	for _, u := range scope {
		if u.Username != username {
			out = append(out, u)
		}
	}
	return out
}

type harness struct {
	fake   *graphtest.Fake
	syncer Syncer
	model  directory.Model
	scope  []ADUser
	opts   Options
	logs   *bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	fake := graphtest.New(t)
	fake.PageSize = 25
	client, err := graph.New(graph.Options{
		BaseURL: fake.URL(), Token: graph.StaticToken(graphtest.Token),
		Timeout: 5 * time.Second, ClientIDSetting: "SEED_GRAPH_CLIENT_ID",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := loadNoord(t)
	logs := &bytes.Buffer{}
	return &harness{
		fake:  fake,
		model: m,
		scope: scopeOf(m),
		logs:  logs,
		syncer: Syncer{
			Client:   client,
			Logger:   slog.New(slog.NewJSONHandler(io.MultiWriter(logs), &slog.HandlerOptions{Level: slog.LevelDebug})),
			Password: func(string) string { return RandomPassword() },
		},
		opts: Options{Domain: testDomain, Phones: ExpectedPhones(m, Demo{}), DeleteLimit: 10},
	}
}

func (h *harness) run(t *testing.T) Result {
	t.Helper()
	res, err := h.syncer.Run(t.Context(), h.scope, h.opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func (h *harness) mustUser(t *testing.T, nickname string) map[string]any {
	t.Helper()
	u, ok := h.fake.User(nickname)
	if !ok {
		t.Fatalf("%s is not in Entra", nickname)
	}
	return u
}
