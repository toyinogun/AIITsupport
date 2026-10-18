package slack

import (
	"os"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

// covers: AC-11. The manifest asks for exactly what the spec allows.
func TestManifest(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/slack/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Features struct {
			AppHome struct {
				MessagesTab bool `yaml:"messages_tab_enabled"`
			} `yaml:"app_home"`
		} `yaml:"features"`
		OAuth struct {
			Scopes struct {
				Bot []string `yaml:"bot"`
			} `yaml:"scopes"`
		} `yaml:"oauth_config"`
		Settings struct {
			Events struct {
				Bot []string `yaml:"bot_events"`
			} `yaml:"event_subscriptions"`
			Interactivity struct {
				Enabled bool `yaml:"is_enabled"`
			} `yaml:"interactivity"`
			SocketMode bool `yaml:"socket_mode_enabled"`
		} `yaml:"settings"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	scopes := slices.Sorted(slices.Values(m.OAuth.Scopes.Bot))
	if want := []string{"app_mentions:read", "chat:write", "im:history", "users:read", "users:read.email"}; !slices.Equal(scopes, want) {
		t.Errorf("bot scopes = %v, want %v", scopes, want)
	}
	events := slices.Sorted(slices.Values(m.Settings.Events.Bot))
	if want := []string{"app_mention", "message.im"}; !slices.Equal(events, want) {
		t.Errorf("bot events = %v, want %v", events, want)
	}
	if !m.Settings.SocketMode || !m.Settings.Interactivity.Enabled || !m.Features.AppHome.MessagesTab {
		t.Errorf("socket mode %t, interactivity %t, messages tab %t; want all on",
			m.Settings.SocketMode, m.Settings.Interactivity.Enabled, m.Features.AppHome.MessagesTab)
	}
}
