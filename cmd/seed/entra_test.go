package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/graph/graphtest"
)

// covers: AC-9 (spec 0005). The clear waits until /methods, which lags
// /phoneMethods, stops listing the phone, and prints that final list.
func TestClearPhonesWaitsForBothLists(t *testing.T) {
	f := graphtest.New(t)
	f.StaleMethodReads = 2
	id := f.AddUser(map[string]any{"mailNickname": "eva.peters", "employeeType": graph.ManagedMarker})
	f.SetPhones(id, []graphtest.Phone{
		{ID: graphtest.PhoneID("mobile"), PhoneNumber: "+1 2025550117", PhoneType: "mobile"},
		{ID: graphtest.PhoneID("office"), PhoneNumber: "+1 2025550198", PhoneType: "office"},
	})
	c, err := graph.New(graph.Options{BaseURL: f.URL(), Token: graph.StaticToken(graphtest.Token), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	u, err := c.UserByUsername(t.Context(), "eva.peters")
	if err != nil {
		t.Fatal(err)
	}

	after, err := clearPhones(t.Context(), c, u, 5*time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("clearPhones: %v", err)
	}
	for _, m := range after {
		if m.Type == "phone" {
			t.Errorf("final list still shows a phone: %+v", after)
		}
	}
	reads := slices.DeleteFunc(f.Requests(), func(r string) bool { return !strings.HasSuffix(r, "/authentication/methods") })
	if len(reads) < 3 {
		t.Errorf("/methods read %d times, want the poll to wait out 2 stale reads", len(reads))
	}
}

// A clear that never settles fails naming the wait setting.
func TestClearPhonesGivesUp(t *testing.T) {
	f := graphtest.New(t)
	f.StaleMethodReads = 1000
	id := f.AddUser(map[string]any{"mailNickname": "eva.peters", "employeeType": graph.ManagedMarker})
	f.SetPhones(id, []graphtest.Phone{{ID: graphtest.PhoneID("mobile"), PhoneNumber: "+1 2025550117", PhoneType: "mobile"}})
	c, err := graph.New(graph.Options{BaseURL: f.URL(), Token: graph.StaticToken(graphtest.Token), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	u, err := c.UserByUsername(t.Context(), "eva.peters")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clearPhones(t.Context(), c, u, 20*time.Millisecond, time.Millisecond); err == nil || !strings.Contains(err.Error(), "SEED_ENTRA_CLEAR_WAIT") {
		t.Errorf("clearPhones = %v, want an error naming SEED_ENTRA_CLEAR_WAIT", err)
	}
}
