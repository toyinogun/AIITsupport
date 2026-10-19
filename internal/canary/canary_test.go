package canary

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// covers: AC-5 (spec 0010). The hunt finds seed names as whole words (case
// sensitive), usernames and emails in any case, and [redacted]; a surname
// that equals a token (Post against kind post) does not false match.
func TestHuntMatchesWholeValues(t *testing.T) {
	h, err := Seed(filepath.Join("..", "..", "seed", "directory", "noord.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	h.Text("my message")
	cases := []struct {
		in   string
		want string // "" means no match
	}{
		{"post", ""},
		{"agent_post_failed", ""},
		{"Requester: Jan de Vries", "Jan de Vries"},
		{"JAN.DEVRIES@NOORD.EXAMPLE", "jan.devries@noord.example"},
		{"user=eva.peters", "eva.peters"},
		{"Dennis Post", "Post"},
		{"x my message y", "my message"},
		{"[redacted]", "[redacted]"},
		{"privileged_request", ""},
	}
	for _, tc := range cases {
		got := h.Find(tc.in)
		switch {
		case tc.want == "" && len(got) > 0:
			t.Errorf("Find(%q) = %v, want nothing", tc.in, got)
		case tc.want != "" && !slices.Contains(got, tc.want):
			t.Errorf("Find(%q) = %v, want %q among them", tc.in, got, tc.want)
		}
	}
	if vals := LogValues([]byte(`{"msg":"x","Jan":"ok","n":{"a":["Eva"]}}` + "\n")); !slices.Contains(vals, "Eva") || slices.Contains(vals, "Jan") {
		t.Errorf("LogValues = %v, want values only, nested ones too", vals)
	}
}

// covers: AC-5 (spec 0010). SpanValues hands the hunt every place a span can
// carry text (name, status, attribute, event, link), and Leaks names each
// planted value and where it was, so a span canary cannot pass by looking
// in the wrong place.
func TestLeaksFindsValuesInEverySpanField(t *testing.T) {
	h, err := Seed(filepath.Join("..", "..", "seed", "directory", "noord.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	spans := tracetest.SpanStubs{{
		Name:       "for Eva Peters",
		Status:     sdktrace.Status{Code: codes.Error, Description: "eva.peters failed"},
		Attributes: []attribute.KeyValue{attribute.String("user", "jan.devries@noord.example")},
		Events:     []sdktrace.Event{{Name: "Bas Kramer", Attributes: []attribute.KeyValue{attribute.String("x", "Ilse Smit")}}},
		Links:      []sdktrace.Link{{Attributes: []attribute.KeyValue{attribute.String("y", "[redacted]")}}},
	}}

	leaks := strings.Join(h.Leaks(SpanValues(spans)), "\n")

	for _, want := range []string{"Eva Peters", "eva.peters", "jan.devries@noord.example", "Bas Kramer", "Ilse Smit", "[redacted]"} {
		if !strings.Contains(leaks, strconv.Quote(want)+" in ") {
			t.Errorf("Leaks missed %q; got:\n%s", want, leaks)
		}
	}
}

// covers: AC-5 (spec 0010). Clean values give no leaks, and a log stream's
// keys never count, only its values.
func TestLeaksIsEmptyForCleanOutput(t *testing.T) {
	h, err := Seed(filepath.Join("..", "..", "seed", "directory", "noord.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	logs := []byte(`{"time":"2026-10-19T20:20:02Z","level":"INFO","msg":"tool_call","tool":"account_unlock","outcome":"ok","Eva":"x"}` + "\n\n")
	spans := tracetest.SpanStubs{{Name: "tool.call", Attributes: []attribute.KeyValue{attribute.String("tool", "account_unlock")}}}

	if leaks := h.Leaks(append(LogValues(logs), SpanValues(spans)...)); len(leaks) > 0 {
		t.Errorf("Leaks = %v, want none", leaks)
	}
}

// covers: AC-5 (spec 0010). A line that is not JSON is hunted whole, so a
// stray fmt.Println cannot hide a name from the canary.
func TestLogValuesKeepsANonJSONLineWhole(t *testing.T) {
	h, err := Seed(filepath.Join("..", "..", "seed", "directory", "noord.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	vals := LogValues([]byte("panic: user Jan de Vries\n"))

	if len(vals) != 1 || len(h.Leaks(vals)) == 0 {
		t.Errorf("LogValues = %v, leaks %v; want the whole line, and the name found", vals, h.Leaks(vals))
	}
}

// covers: AC-5 (spec 0010). A seed file the hunt cannot use stops the
// canary, so it never runs with an empty hunt and passes on nothing.
func TestSeedRefusesAnUnusableFile(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"no users":       "mail_domain: noord.example\nusers: []\n",
		"no mail domain": "users:\n  - {username: a.b, given_name: A, surname: B}\n",
		"not yaml":       "users: [\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".yaml")
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Seed(p); err == nil {
				t.Error("Seed = nil error, want a refusal")
			}
		})
	}
	if _, err := Seed(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("Seed(missing) = nil error, want a refusal")
	}
}
