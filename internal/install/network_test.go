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
// covers: AC-14 (spec 0015). The helpdesk seed Job runs Rails in process, and
// Rails indexes every ticket it writes in Elasticsearch, so the Job's policy
// must open Elasticsearch's port as well as Postgres, Redis, and Memcached
// (journal 0079: the first fresh seed under default deny failed on it).
func TestSeedHelpdeskReachesElasticsearch(t *testing.T) {
	docs := renderPart(t, componentClient, "--set", "zammad.enabled=true")
	np := object(docs, "NetworkPolicy", "seed-helpdesk")
	if np == nil {
		t.Fatal("the client part has no seed-helpdesk NetworkPolicy")
	}
	spec, _ := np["spec"].(map[string]any)
	egress, _ := spec["egress"].([]any)
	want := map[int]bool{8080: false, 5432: false, 6379: false, 11211: false, 9200: false}
	for _, r := range egress {
		rm, _ := r.(map[string]any)
		ports, _ := rm["ports"].([]any)
		for _, p := range ports {
			pm, _ := p.(map[string]any)
			if port, ok := pm["port"].(int); ok {
				if _, tracked := want[port]; tracked {
					want[port] = true
				}
			}
		}
	}
	for port, open := range want {
		if !open {
			t.Errorf("the seed-helpdesk policy does not open port %d to the Zammad pods", port)
		}
	}

	// A connection needs both sides: Elasticsearch has its own policy, and
	// it must admit the seed Job on 9200 (Zammad's own policy only covers
	// Postgres, Redis, Memcached, and the web port).
	es := object(docs, "NetworkPolicy", "elasticsearch")
	if es == nil {
		t.Fatal("the client part has no elasticsearch NetworkPolicy")
	}
	if !admitsSeedOn(es, 9200) {
		t.Error("the elasticsearch policy does not admit the seed-helpdesk pods on port 9200")
	}
}

// admitsSeedOn reports whether a NetworkPolicy has an ingress rule that lets
// the seed-helpdesk pods reach port.
func admitsSeedOn(np map[string]any, port int) bool {
	spec, _ := np["spec"].(map[string]any)
	ingress, _ := spec["ingress"].([]any)
	for _, r := range ingress {
		rm, _ := r.(map[string]any)
		from, _ := rm["from"].([]any)
		var fromSeed bool
		for _, f := range from {
			fm, _ := f.(map[string]any)
			sel, _ := fm["podSelector"].(map[string]any)
			labels, _ := sel["matchLabels"].(map[string]any)
			if labels["app.kubernetes.io/name"] == "seed-helpdesk" {
				fromSeed = true
			}
		}
		ports, _ := rm["ports"].([]any)
		for _, p := range ports {
			pm, _ := p.(map[string]any)
			if got, ok := pm["port"].(int); fromSeed && ok && got == port {
				return true
			}
		}
	}
	return false
}

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
