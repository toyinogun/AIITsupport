package runner

import (
	"slices"
	"testing"

	"github.com/toyinogun/AIITsupport/eval/env"
	"github.com/toyinogun/AIITsupport/internal/graph/graphtest"
)

func TestRegisterAuthenticator(t *testing.T) {
	fake := graphtest.New(t)
	r := &Runner{Env: &env.Env{Entra: &env.Entra{
		Fake:    fake,
		UserIDs: map[string]string{"synced.user": "id-1"},
	}}}

	t.Run("synced user gets the fake method", func(t *testing.T) {
		if err := r.registerAuthenticator("synced.user"); err != nil {
			t.Fatalf("registerAuthenticator: %v", err)
		}
		if got := fake.Authenticators("id-1"); !slices.Equal(got, []string{authenticatorID}) {
			t.Errorf("authenticators = %v, want [%s]", got, authenticatorID)
		}
	})

	// ev-026: the user is synced: false, so the tool refuses with not_in_entra
	// and the setup has nothing to register. It must not fail the run.
	t.Run("user without an Entra account is a no op", func(t *testing.T) {
		if err := r.registerAuthenticator("unsynced.user"); err != nil {
			t.Errorf("registerAuthenticator = %v, want nil", err)
		}
	})
}
