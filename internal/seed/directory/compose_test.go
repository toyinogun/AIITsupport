package directory

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// covers: AC-12, AC-17. The Compose service publishes LDAPS on loopback only,
// adds no capabilities, and keeps no volume besides the read only certs.
func TestComposeSambaService(t *testing.T) {
	raw, err := os.ReadFile("../../../compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Ports   []string `yaml:"ports"`
			CapAdd  []string `yaml:"cap_add"`
			Volumes []string `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatal(err)
	}
	samba, ok := compose.Services["samba"]
	if !ok {
		t.Fatal("compose.yaml has no samba service")
	}
	if len(samba.Ports) != 1 || samba.Ports[0] != "127.0.0.1:55636:636" {
		t.Errorf("samba ports = %v, want only 127.0.0.1:55636:636", samba.Ports)
	}
	if len(samba.CapAdd) != 0 {
		t.Errorf("samba cap_add = %v, want empty", samba.CapAdd)
	}
	if len(samba.Volumes) != 1 || samba.Volumes[0] != "./.dev/certs:/certs:ro" {
		t.Errorf("samba volumes = %v, want only the read only certs", samba.Volumes)
	}
}
