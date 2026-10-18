package helpdesk

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	// A capitalised pair, allowing Dutch name particles in between.
	namePair = regexp.MustCompile(`\b([A-Z][a-z]+)((?: (?:van|de|der|den|El|ter))*) ([A-Z][a-z]+)\b`)
	// A greeting followed by one name: "Hi Yara", "Morning Eva".
	greeting = regexp.MustCompile(`\b(?:Hi|Hello|Morning|Dear|Thanks) ([A-Z][a-z]+)\b`)
	emailRe  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@([A-Za-z0-9.-]+\.[A-Za-z]{2,})`)
	// A login such as eva.peters or jan.devries2 (not a host or file name).
	loginRe = regexp.MustCompile(`\b([a-z]+\.[a-z]+[0-9]?)\b`)
)

// Not logins: file names and host name parts.
var notLogins = map[string]bool{"noord.example": true}

// covers: AC-15. Everything in seed/helpdesk/ is fiction: every person named
// is in noord.yaml, and every email address is under noord.example. The
// name check is a heuristic: a capitalised pair that starts with a Noord
// given name or ends with a Noord surname must be a real Noord user, and a
// name after a greeting must be a Noord given name.
func TestHelpdeskFiction(t *testing.T) {
	_, m := loadNoord(t)
	fullNames := map[string]bool{}
	given := map[string]bool{}
	surnames := map[string]bool{}
	logins := map[string]bool{}
	for _, u := range m.Users {
		fullNames[u.DisplayName()] = true
		given[u.GivenName] = true
		parts := strings.Fields(u.Surname)
		surnames[parts[len(parts)-1]] = true
		logins[u.Username] = true
	}

	err := filepath.WalkDir(helpdeskDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if filepath.Ext(path) != ".md" && filepath.Base(path) != TicketsFile {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // G304: test fixture
		if err != nil {
			return err
		}
		text := string(data)
		rel, _ := filepath.Rel(helpdeskDir, path)

		for _, match := range namePair.FindAllStringSubmatch(text, -1) {
			first, last := match[1], match[3]
			if (given[first] || surnames[last]) && !fullNames[match[0]] {
				t.Errorf("%s names %q, who is not in noord.yaml", rel, match[0])
			}
		}
		for _, match := range greeting.FindAllStringSubmatch(text, -1) {
			if !given[match[1]] {
				t.Errorf("%s greets %q, who is not in noord.yaml", rel, match[1])
			}
		}
		for _, match := range emailRe.FindAllStringSubmatch(text, -1) {
			if host := strings.ToLower(match[1]); host != "noord.example" && !strings.HasSuffix(host, ".noord.example") {
				t.Errorf("%s holds the email %q outside noord.example", rel, match[0])
			}
		}
		for _, match := range loginRe.FindAllStringSubmatch(text, -1) {
			login := match[1]
			if !notLogins[login] && !strings.Contains(text, login+".") && !logins[login] {
				t.Errorf("%s names the login %q, which is not in noord.yaml", rel, login)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The heuristic itself catches an invented person, a stranger's email, and
// an unknown login.
func TestFictionHeuristicCatches(t *testing.T) {
	cases := map[string]*regexp.Regexp{
		"Ask Thomas Smith in Finance": namePair,
		"Hi Gwendolyn,":               greeting,
		"mail bob@example.com":        emailRe,
		"ask john.smith":              loginRe,
	}
	for text, re := range cases {
		if !re.MatchString(text) {
			t.Errorf("%q not matched by %v", text, re)
		}
	}
}
