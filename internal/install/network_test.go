package install

import (
	"slices"
	"testing"
)

// nameOf is a rendered object's metadata.name.
func nameOf(d map[string]any) string {
	meta, _ := d["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	return name
}

// allParts renders the three parts for k3sprox, plus extra arguments.
func allParts(t *testing.T, extra ...string) map[string][]map[string]any {
	t.Helper()
	return map[string][]map[string]any{
		componentAgent:     renderPart(t, componentAgent, extra...),
		componentDirectory: renderPart(t, componentDirectory, extra...),
		componentClient:    renderPart(t, componentClient, append([]string{"--set", "zammad.enabled=true"}, extra...)...),
	}
}

// covers: AC-14 (spec 0015). Every namespace denies all traffic both ways
// unless a policy allows it, and allows DNS to kube-dns.
func TestEveryPartDeniesByDefault(t *testing.T) {
	for part, docs := range allParts(t) {
		deny := object(docs, "NetworkPolicy", "default-deny")
		if deny == nil {
			t.Errorf("the %s part has no default-deny NetworkPolicy", part)
			continue
		}
		spec, _ := deny["spec"].(map[string]any)
		sel, _ := spec["podSelector"].(map[string]any)
		types, _ := spec["policyTypes"].([]any)
		if len(sel) != 0 || !slices.Equal(types, []any{"Ingress", "Egress"}) || spec["ingress"] != nil || spec["egress"] != nil {
			t.Errorf("the %s part's default-deny = %v, want every pod, both ways, no allow", part, spec)
		}
		if object(docs, "NetworkPolicy", "dns") == nil {
			t.Errorf("the %s part has no dns NetworkPolicy", part)
		}
	}
}

// openHTTPS reports whether a NetworkPolicy allows egress to 0.0.0.0/0.
func openHTTPS(d map[string]any) bool {
	spec, _ := d["spec"].(map[string]any)
	egress, _ := spec["egress"].([]any)
	for _, e := range egress {
		em, _ := e.(map[string]any)
		to, _ := em["to"].([]any)
		for _, peer := range to {
			pm, _ := peer.(map[string]any)
			block, _ := pm["ipBlock"].(map[string]any)
			if block["cidr"] == "0.0.0.0/0" {
				return true
			}
		}
	}
	return false
}

// fqdns are the toFQDNs names and patterns of a CiliumNetworkPolicy.
func fqdns(d map[string]any) []string {
	var out []string
	spec, _ := d["spec"].(map[string]any)
	egress, _ := spec["egress"].([]any)
	for _, e := range egress {
		em, _ := e.(map[string]any)
		hosts, _ := em["toFQDNs"].([]any)
		for _, h := range hosts {
			hm, _ := h.(map[string]any)
			for _, k := range []string{"matchName", "matchPattern"} {
				if v, ok := hm[k].(string); ok {
					out = append(out, v)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// covers: AC-14 (spec 0015). With cilium.fqdnEgress, TCP 443 goes only to
// the named hosts: no NetworkPolicy also opens it to every address, since
// allows add up and a Cilium policy cannot narrow one.
func TestFQDNEgressNarrowsHTTPS(t *testing.T) {
	parts := allParts(t) // k3sprox has fqdnEgress on
	want := map[string]map[string][]string{
		componentAgent: {
			"agent-https":      {"*.slack-edge.com", "*.slack.com", "api.anthropic.com", "slack.com"},
			"toolserver-https": {"graph.microsoft.com", "login.microsoftonline.com"},
		},
		componentDirectory: {
			"seed-entra-https": {"graph.microsoft.com", "login.microsoftonline.com"},
		},
	}
	for part, docs := range parts {
		for _, d := range docs {
			if d["kind"] == "NetworkPolicy" && openHTTPS(d) {
				t.Errorf("the %s part's NetworkPolicy %s opens 443 to every address", part, nameOf(d))
			}
		}
		for name, hosts := range want[part] {
			cnp := object(docs, "CiliumNetworkPolicy", name)
			if cnp == nil {
				t.Errorf("the %s part has no CiliumNetworkPolicy %s", part, name)
				continue
			}
			if got := fqdns(cnp); !slices.Equal(got, hosts) {
				t.Errorf("%s hosts = %v, want %v", name, got, hosts)
			}
		}
	}
}

// covers: AC-14 (spec 0015). Without Cilium, the same workloads reach any
// address outside the cluster's own ranges on 443, and no Cilium kind
// renders (the cluster has no such CRD).
func TestHTTPSWithoutCilium(t *testing.T) {
	parts := allParts(t, "--set", "cilium.enabled=false", "--set", "cilium.fqdnEgress=false")
	for part, docs := range parts {
		for _, d := range docs {
			if d["kind"] == "CiliumNetworkPolicy" {
				t.Errorf("the %s part renders CiliumNetworkPolicy %s without Cilium", part, nameOf(d))
			}
		}
	}
	np := object(parts[componentAgent], "NetworkPolicy", "agent-https")
	if np == nil || !openHTTPS(np) {
		t.Fatalf("agent-https = %v, want a NetworkPolicy to 0.0.0.0/0", np)
	}
	spec, _ := np["spec"].(map[string]any)
	egress, _ := spec["egress"].([]any)
	rule, _ := egress[0].(map[string]any)
	to, _ := rule["to"].([]any)
	peer, _ := to[0].(map[string]any)
	block, _ := peer["ipBlock"].(map[string]any)
	except, _ := block["except"].([]any)
	for _, cidr := range []any{"10.42.0.0/16", "10.43.0.0/16", "172.16.70.0/24"} {
		if !slices.Contains(except, cidr) {
			t.Errorf("agent-https except = %v, lacks %v", except, cidr)
		}
	}
}

// covers: AC-14, AC-15 (spec 0015). Only the reset page's tailnet proxy
// reaches the tool server's reset port, and nothing else reaches past 8080.
func TestResetPortOnlyFromTailnetProxy(t *testing.T) {
	docs := renderPart(t, componentAgent)
	np := object(docs, "NetworkPolicy", "toolserver")
	if np == nil {
		t.Fatal("the agent part has no toolserver NetworkPolicy")
	}
	spec, _ := np["spec"].(map[string]any)
	ingress, _ := spec["ingress"].([]any)
	var resetRules int
	for _, r := range ingress {
		rm, _ := r.(map[string]any)
		ports, _ := rm["ports"].([]any)
		from, _ := rm["from"].([]any)
		for _, p := range ports {
			pm, _ := p.(map[string]any)
			switch pm["port"] {
			case 8080:
			case 8081:
				resetRules++
				peer, _ := from[0].(map[string]any)
				pods, _ := peer["podSelector"].(map[string]any)
				labels, _ := pods["matchLabels"].(map[string]any)
				if len(from) != 1 || labels["tailscale.com/parent-resource"] != "reset" || labels["tailscale.com/parent-resource-ns"] != "noord-agent" {
					t.Errorf("the reset port is reachable from %v, want only the reset Ingress's proxy", from)
				}
			default:
				t.Errorf("the tool server allows ingress on port %v", pm["port"])
			}
		}
	}
	if resetRules != 1 {
		t.Errorf("%d rules allow the reset port, want 1", resetRules)
	}
}
