// Package canary is a test helper for spec 0010's canary tests (AC-5): it
// loads every person in seed/directory/noord.yaml and hunts for them, and
// for a flow's own texts, in the string values of log lines and spans. It
// reads the seed file itself, so the agent's tests can use it without
// importing internal/seed (which the credential boundary denies).
package canary

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"gopkg.in/yaml.v3"

	"github.com/toyinogun/AIITsupport/internal/logging"
)

// Hunt is the set of values no log line or span may hold.
type Hunt struct {
	words  []*regexp.Regexp // whole word matches
	labels []string
	texts  []string // plain substrings: message, ticket, and model texts
}

type seedFile struct {
	MailDomain string `yaml:"mail_domain"`
	Users      []struct {
		Username  string `yaml:"username"`
		GivenName string `yaml:"given_name"`
		Surname   string `yaml:"surname"`
	} `yaml:"users"`
	Admins []struct {
		Username string `yaml:"username"`
	} `yaml:"admins"`
}

// Seed returns a hunt for every display name, given name, and surname in
// the seed file (case sensitive, whole word) and every username and email
// (case insensitive, whole word).
func Seed(path string) (*Hunt, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a test helper reading the seed path its caller names
	if err != nil {
		return nil, fmt.Errorf("read seed: %w", err)
	}
	var f seedFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse seed: %w", err)
	}
	if len(f.Users) == 0 || f.MailDomain == "" {
		return nil, fmt.Errorf("parse seed: no users or mail domain in %s", path)
	}
	h := &Hunt{}
	for _, u := range f.Users {
		h.Word(u.GivenName + " " + u.Surname)
		h.Word(u.GivenName)
		h.Word(u.Surname)
		h.AnyCase(u.Username)
		h.AnyCase(u.Username + "@" + f.MailDomain)
	}
	for _, a := range f.Admins {
		h.AnyCase(a.Username)
		h.AnyCase(a.Username + "@" + f.MailDomain)
	}
	return h, nil
}

// Word adds a case sensitive whole word value.
func (h *Hunt) Word(v string) { h.add(v, "") }

// AnyCase adds a case insensitive whole word value.
func (h *Hunt) AnyCase(v string) { h.add(v, "(?i)") }

// Text adds a value matched anywhere, as is.
func (h *Hunt) Text(v string) { h.texts = append(h.texts, v) }

func (h *Hunt) add(v, flags string) {
	if v == "" {
		return
	}
	h.words = append(h.words, regexp.MustCompile(flags+`(?:^|[^\pL\pN_])`+regexp.QuoteMeta(v)+`(?:$|[^\pL\pN_])`))
	h.labels = append(h.labels, v)
}

// Find returns the hunted values s holds, and Redacted if it holds that.
func (h *Hunt) Find(s string) []string {
	var found []string
	for i, re := range h.words {
		if re.MatchString(s) {
			found = append(found, h.labels[i])
		}
	}
	for _, t := range h.texts {
		if strings.Contains(s, t) {
			found = append(found, t)
		}
	}
	if strings.Contains(s, logging.Redacted) {
		found = append(found, logging.Redacted)
	}
	return found
}

// LogValues returns every JSON string value in a stream of JSON log lines,
// never a key. A line that is not JSON is returned whole.
func LogValues(logs []byte) []string {
	var out []string
	for _, line := range bytes.Split(logs, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(line, &v); err != nil {
			out = append(out, string(line))
			continue
		}
		out = appendStrings(out, v)
	}
	return out
}

func appendStrings(out []string, v any) []string {
	switch x := v.(type) {
	case string:
		return append(out, x)
	case map[string]any:
		for _, e := range x {
			out = appendStrings(out, e)
		}
	case []any:
		for _, e := range x {
			out = appendStrings(out, e)
		}
	}
	return out
}

// SpanValues returns every string an exported span carries: its name,
// attribute values, status description, events, and link attributes.
func SpanValues(spans tracetest.SpanStubs) []string {
	var out []string
	for _, s := range spans {
		out = append(out, s.Name, s.Status.Description)
		for _, a := range s.Attributes {
			out = append(out, a.Value.String())
		}
		for _, e := range s.Events {
			out = append(out, e.Name)
			for _, a := range e.Attributes {
				out = append(out, a.Value.String())
			}
		}
		for _, l := range s.Links {
			for _, a := range l.Attributes {
				out = append(out, a.Value.String())
			}
		}
	}
	return out
}

// Leaks returns "value in: string" for each hunted value in values.
func (h *Hunt) Leaks(values []string) []string {
	var out []string
	for _, v := range values {
		for _, f := range h.Find(v) {
			out = append(out, fmt.Sprintf("%q in %q", f, v))
		}
	}
	return out
}
