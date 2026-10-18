package entra

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/graph/graphtest"
)

// covers: AC-2, AC-3, AC-4, AC-5. A fresh tenant ends with every synced user
// managed, mapped, phoned, and linked to its manager; the gap and the
// tenant admin are untouched.
func TestSyncFreshTenant(t *testing.T) {
	h := newHarness(t)
	adminID := h.fake.AddUser(map[string]any{"mailNickname": "tenantadmin", "userPrincipalName": "tenantadmin@" + testDomain})

	res := h.run(t)
	if res.Created != len(h.scope) || res.PhonesSet != len(h.scope) {
		t.Errorf("created %d, phones %d; want %d each", res.Created, res.PhonesSet, len(h.scope))
	}

	eva := h.mustUser(t, "eva.peters")
	want := map[string]any{
		"userPrincipalName": "eva.peters@" + testDomain, "displayName": "Eva Peters", "givenName": "Eva",
		"surname": "Peters", "department": "Sales", "jobTitle": "Account Manager", "accountEnabled": true,
		"companyName": CompanyName, "employeeType": graph.ManagedMarker, "usageLocation": UsageLocation,
	}
	for k, v := range want {
		if eva[k] != v {
			t.Errorf("eva.peters %s = %v, want %v", k, eva[k], v)
		}
	}
	evaID, _ := eva["id"].(string)
	phones := h.fake.Phones(evaID)
	if len(phones) != 1 || phones[0].PhoneType != "mobile" || phones[0].PhoneNumber != h.opts.Phones["eva.peters"] {
		t.Errorf("eva.peters phones = %+v, want one mobile %s", phones, h.opts.Phones["eva.peters"])
	}
	daan := h.mustUser(t, "daan.bos")
	if h.fake.Manager(evaID) != daan["id"] {
		t.Errorf("eva.peters manager = %q, want daan.bos %v", h.fake.Manager(evaID), daan["id"])
	}

	for _, u := range h.model.Users {
		if _, ok := h.fake.User(u.Username); ok != u.Synced {
			t.Errorf("%s in Entra = %t, want %t (synced)", u.Username, ok, u.Synced)
		}
	}
	if _, ok := h.fake.User("adm.ruben.mulder"); ok {
		t.Error("the admin account reached Entra")
	}
	if admin, _ := h.fake.User("tenantadmin"); admin["employeeType"] != nil || len(h.fake.Phones(adminID)) != 0 {
		t.Errorf("the tenant admin was written: %v", admin)
	}
}

// covers: AC-6. A second run with nothing changed makes zero Graph writes.
func TestSyncRerunMakesZeroWrites(t *testing.T) {
	h := newHarness(t)
	h.run(t)
	before := h.fake.Writes()

	res := h.run(t)
	if res.Writes != 0 || h.fake.Writes() != before {
		t.Errorf("rerun wrote %d (fake saw %d): %+v", res.Writes, h.fake.Writes()-before, res)
	}
}

// covers: AC-6. Drift is put back, and a deleted user is restored with the
// same object id rather than created again.
func TestSyncRepairsDrift(t *testing.T) {
	h := newHarness(t)
	h.run(t)

	eva := h.mustUser(t, "eva.peters")
	evaID, _ := eva["id"].(string)
	h.fake.Patch(evaID, map[string]any{"department": "Marketing"})
	bas := h.mustUser(t, "bas.kramer")
	basID, _ := bas["id"].(string)
	h.fake.SetPhones(basID, nil)
	daan := h.mustUser(t, "daan.bos")
	daanID, _ := daan["id"].(string)
	h.fake.SoftDelete(daanID)

	res := h.run(t)
	if res.Restored != 1 || res.Created != 0 || res.Updated != 1 {
		t.Errorf("result = %+v, want 1 restored, 0 created, 1 updated", res)
	}
	if got := h.mustUser(t, "eva.peters")["department"]; got != "Sales" {
		t.Errorf("eva.peters department = %v, want Sales", got)
	}
	if len(h.fake.Phones(basID)) != 1 {
		t.Errorf("bas.kramer phones = %v, want the mobile back", h.fake.Phones(basID))
	}
	if got := h.mustUser(t, "daan.bos")["id"]; got != daanID {
		t.Errorf("daan.bos id = %v, want the restored %s", got, daanID)
	}
	if h.fake.Manager(evaID) != daanID {
		t.Errorf("eva.peters manager = %q, want daan.bos back", h.fake.Manager(evaID))
	}

	if res := h.run(t); res.Writes != 0 {
		t.Errorf("run after repair wrote %d: %+v", res.Writes, res)
	}
}

// covers: AC-7. A user leaving scope is soft deleted.
func TestSyncDeletesOutOfScope(t *testing.T) {
	h := newHarness(t)
	h.run(t)
	h.scope = without(h.scope, "eva.peters")

	res := h.run(t)
	if res.Deleted != 1 {
		t.Errorf("deleted = %d, want 1", res.Deleted)
	}
	if _, ok := h.fake.Deleted("eva.peters"); !ok {
		t.Error("eva.peters is not in the recycle bin")
	}
}

// covers: AC-7. More deletes than the limit exit before any write.
func TestSyncDeleteLimit(t *testing.T) {
	h := newHarness(t)
	h.run(t)
	for _, u := range h.scope[:11] {
		h.scope = without(h.scope, u.Username)
	}
	before := h.fake.Writes()

	_, err := h.syncer.Run(t.Context(), h.scope, h.opts)
	if err == nil || !strings.Contains(err.Error(), "SEED_ENTRA_DELETE_LIMIT") {
		t.Fatalf("Run = %v, want the delete limit error", err)
	}
	if h.fake.Writes() != before {
		t.Errorf("%d writes before the limit stopped the run", h.fake.Writes()-before)
	}
}

// covers: AC-7. An unmanaged user holding an in scope nickname stops the
// run before any write, and is never touched.
func TestSyncUnmanagedCollision(t *testing.T) {
	h := newHarness(t)
	h.fake.AddUser(map[string]any{"mailNickname": "eva.peters", "userPrincipalName": "someone@" + testDomain})

	_, err := h.syncer.Run(t.Context(), h.scope, h.opts)
	if err == nil || !strings.Contains(err.Error(), "unmanaged Entra user someone@") {
		t.Fatalf("Run = %v, want the collision named", err)
	}
	if h.fake.Writes() != 0 {
		t.Errorf("%d writes despite the collision", h.fake.Writes())
	}
}

// covers: AC-7. Two managed users sharing a nickname (live plus deleted)
// stop the run before any write.
func TestSyncDuplicateManagedNickname(t *testing.T) {
	h := newHarness(t)
	h.fake.AddUser(map[string]any{"mailNickname": "eva.peters", "employeeType": graph.ManagedMarker, "userPrincipalName": "eva.peters@" + testDomain})
	h.fake.AddDeleted(map[string]any{"mailNickname": "eva.peters", "employeeType": graph.ManagedMarker, "userPrincipalName": "x@" + testDomain})

	_, err := h.syncer.Run(t.Context(), h.scope, h.opts)
	if err == nil || !strings.Contains(err.Error(), "share mailNickname eva.peters") {
		t.Fatalf("Run = %v, want the duplicate named", err)
	}
	if h.fake.Writes() != 0 {
		t.Errorf("%d writes despite the duplicate", h.fake.Writes())
	}
}

// covers: AC-3. A group member outside OU=Users stops the run before any write.
func TestSyncScopeGuard(t *testing.T) {
	h := newHarness(t)
	h.scope = append(h.scope, ADUser{DN: "CN=adm.ruben.mulder,OU=Admin Accounts,OU=Noord," + testBase, Username: "adm.ruben.mulder", Outside: true})

	_, err := h.syncer.Run(t.Context(), h.scope, h.opts)
	if err == nil || !strings.Contains(err.Error(), "OU=Admin Accounts") {
		t.Fatalf("Run = %v, want the admin account named", err)
	}
	if h.fake.Writes() != 0 {
		t.Errorf("%d writes despite the scope guard", h.fake.Writes())
	}
}

// covers: AC-4. A manager in the sync gap leaves manager empty and is logged.
func TestSyncManagerNotInScope(t *testing.T) {
	h := newHarness(t)
	h.run(t)
	eva := h.mustUser(t, "eva.peters")
	evaID, _ := eva["id"].(string)
	if h.fake.Manager(evaID) == "" {
		t.Fatal("eva.peters has no manager after the first run")
	}

	h.scope = without(h.scope, "daan.bos")
	h.opts.DeleteLimit = 10
	res := h.run(t)
	if h.fake.Manager(evaID) != "" {
		t.Errorf("eva.peters manager = %q, want empty once daan.bos leaves scope", h.fake.Manager(evaID))
	}
	if res.ManagersCleared == 0 || res.ManagersSkipped == 0 {
		t.Errorf("result = %+v, want managers cleared and skipped", res)
	}
	if !strings.Contains(h.logs.String(), `"msg":"manager not in scope","user":"eva.peters","manager":"daan.bos"`) {
		t.Errorf("no manager skip log line for eva.peters in:\n%s", h.logs.String())
	}
}

// covers: AC-4, AC-7. --only touches one user: no deletes, and a manager
// that is not in Entra yet is skipped.
func TestSyncOnly(t *testing.T) {
	h := newHarness(t)
	h.fake.AddUser(map[string]any{"mailNickname": "stale.user", "employeeType": graph.ManagedMarker, "userPrincipalName": "stale.user@" + testDomain})
	h.opts.Only = "eva.peters"

	res := h.run(t)
	if res.Created != 1 || res.Deleted != 0 || res.ManagersSet != 0 || res.ManagersSkipped != 1 {
		t.Errorf("result = %+v, want 1 created, nothing deleted, the manager skipped", res)
	}
	if _, ok := h.fake.User("stale.user"); !ok {
		t.Error("--only deleted an out of scope user")
	}
	if !strings.Contains(h.logs.String(), ReasonNotInEntra) {
		t.Errorf("no %q log line", ReasonNotInEntra)
	}

	h.opts.Only = "nobody.here"
	if _, err := h.syncer.Run(t.Context(), h.scope, h.opts); err == nil {
		t.Error("--only with a user outside SYNC-Entra-Users: want an error")
	}
}

// covers: AC-12. A create that clashes with a managed user the listing
// missed continues as an update.
func TestSyncCreateConflictContinuesAsUpdate(t *testing.T) {
	h := newHarness(t)
	h.opts.Only = "eva.peters"
	var once atomic.Bool
	h.fake.Intercept = func(r *http.Request) (graphtest.Fault, bool) {
		if r.Method == http.MethodPost && r.URL.Path == "/users" && once.CompareAndSwap(false, true) {
			h.fake.AddUser(map[string]any{"mailNickname": "eva.peters", "employeeType": graph.ManagedMarker, "userPrincipalName": "eva.peters@" + testDomain})
			return graphtest.Fault{
				Status: http.StatusBadRequest, Code: "Request_BadRequest",
				Message: "Another object with the same value for property userPrincipalName already exists.",
			}, true
		}
		return graphtest.Fault{}, false
	}

	res := h.run(t)
	if res.Created != 0 || res.Updated != 1 || res.PhonesSet != 1 {
		t.Errorf("result = %+v, want the clash resolved as an update with a phone", res)
	}
	if got := h.mustUser(t, "eva.peters")["displayName"]; got != "Eva Peters" {
		t.Errorf("displayName = %v after the update", got)
	}
}

// covers: AC-7, AC-12. A create clashing with an unmanaged user stops.
func TestSyncCreateConflictWithUnmanaged(t *testing.T) {
	h := newHarness(t)
	h.opts.Only = "eva.peters"
	h.fake.Intercept = func(r *http.Request) (graphtest.Fault, bool) {
		if r.Method == http.MethodPost && r.URL.Path == "/users" {
			if _, ok := h.fake.User("eva.peters"); !ok {
				h.fake.AddUser(map[string]any{"mailNickname": "eva.peters", "userPrincipalName": "eva.peters@" + testDomain})
			}
			return graphtest.Fault{Status: http.StatusConflict, Code: "ObjectConflict"}, true
		}
		return graphtest.Fault{}, false
	}
	if _, err := h.syncer.Run(t.Context(), h.scope, h.opts); !errors.Is(err, graph.ErrNotManaged) {
		t.Errorf("Run = %v, want ErrNotManaged", err)
	}
}

// covers: AC-5, AC-12. A mobile POST that clashes reads the methods again
// and PATCHes; other phone types are never touched.
func TestSyncPhoneConflictThenPatch(t *testing.T) {
	h := newHarness(t)
	h.opts.Only = "eva.peters"
	var once atomic.Bool
	h.fake.Intercept = func(r *http.Request) (graphtest.Fault, bool) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/phoneMethods") && once.CompareAndSwap(false, true) {
			id := strings.Split(r.URL.Path, "/")[2]
			h.fake.SetPhones(id, []graphtest.Phone{
				{ID: graphtest.PhoneID("mobile"), PhoneNumber: "+1 2025550199", PhoneType: "mobile"},
				{ID: graphtest.PhoneID("office"), PhoneNumber: "+1 2025550198", PhoneType: "office"},
			})
			return graphtest.Fault{Status: http.StatusBadRequest, Message: "A phone method of this type already exists."}, true
		}
		return graphtest.Fault{}, false
	}

	h.run(t)
	eva := h.mustUser(t, "eva.peters")
	evaID, _ := eva["id"].(string)
	phones := h.fake.Phones(evaID)
	if len(phones) != 2 || phones[0].PhoneNumber != h.opts.Phones["eva.peters"] || phones[1].PhoneNumber != "+1 2025550198" {
		t.Errorf("phones = %+v, want the mobile fixed and the office number untouched", phones)
	}
}

// covers: AC-5. A wrong mobile number is PATCHed, not added again.
func TestSyncFixesWrongMobile(t *testing.T) {
	h := newHarness(t)
	h.run(t)
	eva := h.mustUser(t, "eva.peters")
	evaID, _ := eva["id"].(string)
	h.fake.SetPhones(evaID, []graphtest.Phone{{ID: graphtest.PhoneID("mobile"), PhoneNumber: "+1 2025550199", PhoneType: "mobile"}})

	res := h.run(t)
	if res.PhonesSet != 1 || res.Writes != 1 {
		t.Errorf("result = %+v, want exactly one phone write", res)
	}
	if got := h.fake.Phones(evaID); len(got) != 1 || got[0].PhoneNumber != h.opts.Phones["eva.peters"] {
		t.Errorf("phones = %+v", got)
	}
}

// covers: AC-8. Demo users get the demo password at create; a rerun never
// rewrites a password.
func TestSyncDemoPasswordAtCreate(t *testing.T) {
	h := newHarness(t)
	demo := Demo{Users: []DemoUser{{Username: "eva.peters", MFAPhone: "+1 2025550195"}}}
	const demoPassword = "Kq7!vXz2-Wm9#"
	h.syncer.Password = func(u string) string {
		if demo.IsDemo(u) {
			return demoPassword
		}
		return RandomPassword()
	}
	h.opts.Phones = mustPhones(t, h.model, demo)
	h.run(t)

	eva := h.mustUser(t, "eva.peters")
	evaID, _ := eva["id"].(string)
	if pw, _ := h.fake.Password(evaID); pw != demoPassword {
		t.Error("eva.peters did not get the demo password at create")
	}
	if got := h.fake.Phones(evaID); len(got) != 1 || got[0].PhoneNumber != "+1 2025550195" {
		t.Errorf("eva.peters phones = %+v, want the demo number", got)
	}
	bas := h.mustUser(t, "bas.kramer")
	basID, _ := bas["id"].(string)
	if pw, _ := h.fake.Password(basID); pw == demoPassword || len(pw) != passwordLen {
		t.Error("bas.kramer did not get a fresh random password")
	}
	if strings.Contains(h.logs.String(), demoPassword) || strings.Contains(h.logs.String(), "+1 2025550195") {
		t.Error("a log line holds the demo password or the demo phone")
	}
}

// covers: AC-12. Graph replication lag on a phone POST right after create
// is retried.
func TestSyncRetriesNotFoundAfterCreate(t *testing.T) {
	h := newHarness(t)
	h.opts.Only = "eva.peters"
	client, err := graph.New(graph.Options{
		BaseURL: h.fake.URL(), Token: graph.StaticToken(graphtest.Token), MaxRetries: 2, Timeout: 5e9,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.syncer.Client = client
	var once atomic.Bool
	h.fake.Intercept = func(r *http.Request) (graphtest.Fault, bool) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/phoneMethods") && once.CompareAndSwap(false, true) {
			return graphtest.Fault{Status: http.StatusNotFound, RetryAfter: "1", Code: "Request_ResourceNotFound"}, true
		}
		return graphtest.Fault{}, false
	}
	if res := h.run(t); res.PhonesSet != 1 {
		t.Errorf("result = %+v, want the phone set after one retry", res)
	}
}

// covers: AC-5. The fake refuses an unallocated range the way Graph did
// (journal 0013), so a generator regression fails here, not on the tenant.
func TestSyncRefusesUnallocatedPhoneRange(t *testing.T) {
	h := newHarness(t)
	h.opts.Only = "eva.peters"
	for _, bad := range []string{"+31 600000014", "+44 7700900014"} {
		h.opts.Phones = map[string]string{"eva.peters": bad}
		_, err := h.syncer.Run(t.Context(), h.scope, h.opts)
		if err == nil || !strings.Contains(err.Error(), "invalidPhoneNumber") {
			t.Errorf("Run with %s = %v, want Graph's invalidPhoneNumber", bad, err)
		}
	}
}
