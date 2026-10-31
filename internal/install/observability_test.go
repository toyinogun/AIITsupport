package install

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const filesDir = "deploy/helm/noord/files/observability"

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, filesDir, name)) //nolint:gosec // a fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// obsParts renders the agent part and the three component parts, each with
// only its own chart on, as the bootstrap chart's Applications do.
func obsParts(t *testing.T, extra ...string) map[string][]map[string]any {
	t.Helper()
	return map[string][]map[string]any{
		componentAgent:     renderPart(t, componentAgent, extra...),
		componentCollector: renderPart(t, componentCollector, append([]string{"--set", "collector.enabled=true"}, extra...)...),
		componentTempo:     renderPart(t, componentTempo, append([]string{"--set", "tempo.enabled=true"}, extra...)...),
		componentGrafana:   renderPart(t, componentGrafana, append([]string{"--set", "grafana.enabled=true"}, extra...)...),
	}
}

func dataOf(d map[string]any) map[string]any {
	data, _ := d["data"].(map[string]any)
	return data
}

// covers: spec 0016 AC-11, AC-12. noord-agent owns the ConfigMaps the other
// Applications mount, and each holds exactly the file in the chart.
func TestObservabilityConfigMapsAreTheFiles(t *testing.T) {
	docs := renderPart(t, componentAgent)
	for name, want := range map[string]struct{ key, file string }{
		"noord-otel-collector":      {"relay", "collector.yaml"},
		"noord-grafana-datasources": {"datasources.yaml", "datasources.yaml"},
		"noord-grafana-dashboards":  {"noord-ops.json", "dashboards/noord-ops.json"},
	} {
		cm := object(docs, "ConfigMap", name)
		if cm == nil {
			t.Errorf("the agent part renders no ConfigMap %s", name)
			continue
		}
		got, _ := dataOf(cm)[want.key].(string)
		if strings.TrimSpace(got) != strings.TrimSpace(readFile(t, want.file)) {
			t.Errorf("ConfigMap %s key %s differs from files/observability/%s", name, want.key, want.file)
		}
	}
}

// covers: spec 0016 AC-12. Each component Application renders only its own
// chart, and its pods run as the restricted Pod Security level asks: non root,
// no privilege escalation, every capability dropped, a read only root
// filesystem, no privileged container, no host access.
func TestObservabilityPodsAreRestricted(t *testing.T) {
	parts := obsParts(t)
	want := map[string]struct{ kind, name string }{
		componentCollector: {"Deployment", "otel-collector"},
		componentTempo:     {"StatefulSet", "tempo"},
		componentGrafana:   {"Deployment", "grafana"},
	}
	for part, w := range want {
		docs := parts[part]
		var workloads []string
		for _, d := range docs {
			if k, _ := d["kind"].(string); k == "Deployment" || k == "StatefulSet" || k == "DaemonSet" || k == "Job" {
				workloads = append(workloads, k+"/"+nameOf(d))
			}
		}
		if !slices.Equal(workloads, []string{w.kind + "/" + w.name}) {
			t.Errorf("the %s part renders workloads %v, want only %s/%s", part, workloads, w.kind, w.name)
		}
		wl := object(docs, w.kind, w.name)
		pod := podSpec(wl)
		psc, _ := pod["securityContext"].(map[string]any)
		if psc["runAsNonRoot"] != true {
			t.Errorf("%s: pod securityContext = %v, want runAsNonRoot", part, psc)
		}
		for _, k := range []string{"hostNetwork", "hostPID", "hostIPC"} {
			if pod[k] == true {
				t.Errorf("%s: %s is on", part, k)
			}
		}
		for _, list := range []string{"containers", "initContainers"} {
			cs, _ := pod[list].([]any)
			for _, c := range cs {
				cm, _ := c.(map[string]any)
				sc, _ := cm["securityContext"].(map[string]any)
				caps, _ := sc["capabilities"].(map[string]any)
				drop, _ := caps["drop"].([]any)
				if sc["allowPrivilegeEscalation"] != false || sc["readOnlyRootFilesystem"] != true || sc["privileged"] == true ||
					len(drop) != 1 || drop[0] != "ALL" {
					t.Errorf("%s: container %v securityContext = %v, want restricted with a read only root", part, cm["name"], sc)
				}
			}
		}
		for _, d := range docs {
			if d["kind"] == "Secret" {
				t.Errorf("%s renders a plain Secret %s", part, nameOf(d))
			}
		}
	}
}

// covers: spec 0016 AC-5, AC-12. The Collector runs our config, not the
// chart's, and the agent and the tool server send to it.
func TestObservabilityWiring(t *testing.T) {
	parts := obsParts(t)
	col := object(parts[componentCollector], "Deployment", "otel-collector")
	vols, _ := podSpec(col)["volumes"].([]any)
	var cmName string
	for _, v := range vols {
		vm, _ := v.(map[string]any)
		if cm, ok := vm["configMap"].(map[string]any); ok && vm["name"] == "collector-configmap" {
			cmName, _ = cm["name"].(string)
		}
	}
	if cmName != "noord-otel-collector" {
		t.Errorf("the Collector mounts ConfigMap %q, want noord-otel-collector (our file)", cmName)
	}
	if got := object(parts[componentCollector], "ConfigMap", ""); got != nil {
		t.Errorf("the collector part renders a ConfigMap %s of its own", nameOf(got))
	}
	for _, workload := range []string{"agent", "toolserver"} {
		var found string
		for _, e := range envList(parts[componentAgent], "Deployment", workload, workload) {
			if e["name"] == "OTEL_EXPORTER_OTLP_ENDPOINT" {
				found, _ = e["value"].(string)
			}
		}
		if found != "http://otel-collector.noord-agent.svc:4318" {
			t.Errorf("%s sends traces to %q, want the Collector's OTLP HTTP port", workload, found)
		}
	}
	tempo := object(parts[componentTempo], "ConfigMap", "tempo")
	if !strings.Contains(dataOf(tempo)["tempo.yaml"].(string), "block_retention: 168h") {
		t.Errorf("Tempo's config does not keep blocks for 168h:\n%v", dataOf(tempo)["tempo.yaml"])
	}
	sts := object(parts[componentTempo], "StatefulSet", "tempo")
	spec, _ := sts["spec"].(map[string]any)
	if pol, _ := spec["persistentVolumeClaimRetentionPolicy"].(map[string]any); pol["whenDeleted"] != "Delete" {
		t.Errorf("Tempo's claim policy = %v, want the claim deleted with the StatefulSet", pol)
	}
}

// envList is the env entries of a container, in order.
func envList(docs []map[string]any, kind, prefix, container string) []map[string]any {
	var out []map[string]any
	if d := object(docs, kind, prefix); d != nil {
		cs, _ := podSpec(d)["containers"].([]any)
		for _, c := range cs {
			cm, _ := c.(map[string]any)
			if cm["name"] != container {
				continue
			}
			env, _ := cm["env"].([]any)
			for _, e := range env {
				em, _ := e.(map[string]any)
				out = append(out, em)
			}
		}
	}
	return out
}

// ingressFrom reports whether np has an ingress rule on port from pods that
// match the label (key=value), or from the tailnet proxy of the named Ingress.
func ingressFrom(np map[string]any, port int, label, proxyOf string) bool {
	spec, _ := np["spec"].(map[string]any)
	rules, _ := spec["ingress"].([]any)
	for _, r := range rules {
		rm, _ := r.(map[string]any)
		ports, _ := rm["ports"].([]any)
		onPort := false
		for _, p := range ports {
			if pm, _ := p.(map[string]any); pm["port"] == port {
				onPort = true
			}
		}
		from, _ := rm["from"].([]any)
		for _, f := range from {
			fm, _ := f.(map[string]any)
			sel, _ := fm["podSelector"].(map[string]any)
			labels, _ := sel["matchLabels"].(map[string]any)
			if onPort && label != "" && labels["app.kubernetes.io/name"] == label {
				return true
			}
			if onPort && proxyOf != "" && labels["tailscale.com/parent-resource"] == proxyOf {
				return true
			}
		}
	}
	return false
}

// covers: spec 0016 AC-12. The default deny gains exactly these allows: the
// agent and the tool server to the Collector (4318), the Collector to Tempo
// (4317), Grafana to Tempo (3200) and to the database (5432), and the
// Tailscale ingress to Grafana. Nothing else reaches the three workloads.
func TestObservabilityNetworkAllows(t *testing.T) {
	docs := renderPart(t, componentAgent)
	policy := func(name string) map[string]any {
		np := object(docs, "NetworkPolicy", name)
		if np == nil {
			t.Fatalf("no NetworkPolicy %s", name)
		}
		return np
	}
	for _, w := range []string{"agent", "toolserver"} {
		if !egressReaches(policy(w), "otel-collector", 4318) {
			t.Errorf("%s has no egress to the Collector on 4318", w)
		}
	}
	col := policy("otel-collector")
	if !ingressFrom(col, 4318, "agent", "") || !ingressFrom(col, 4318, "toolserver", "") || !egressReaches(col, "tempo", 4317) {
		t.Errorf("the Collector policy = %v, want agent and tool server in on 4318 and Tempo out on 4317", col["spec"])
	}
	tempo := policy("tempo")
	if !ingressFrom(tempo, 4317, "otel-collector", "") || !ingressFrom(tempo, 3200, "grafana", "") {
		t.Errorf("Tempo's policy = %v, want the Collector in on 4317 and Grafana in on 3200", tempo["spec"])
	}
	if spec, _ := tempo["spec"].(map[string]any); spec["egress"] != nil {
		t.Errorf("Tempo may send traffic out: %v", spec["egress"])
	}
	graf := policy("grafana")
	if !ingressFrom(graf, 3000, "", "grafana") || !egressReaches(graf, "tempo", 3200) {
		t.Errorf("Grafana's policy = %v, want only its tailnet proxy in and Tempo out", graf["spec"])
	}
	if spec, _ := graf["spec"].(map[string]any); len(spec["ingress"].([]any)) != 1 {
		t.Errorf("Grafana has %d ingress rules, want only the Tailscale proxy", len(spec["ingress"].([]any)))
	}
	var toDB bool
	for _, r := range graf["spec"].(map[string]any)["egress"].([]any) {
		rm, _ := r.(map[string]any)
		for _, to := range rm["to"].([]any) {
			sel, _ := to.(map[string]any)["podSelector"].(map[string]any)
			if labels, _ := sel["matchLabels"].(map[string]any); labels["cnpg.io/cluster"] == "noord-pg" {
				toDB = true
			}
		}
	}
	if !toDB {
		t.Error("Grafana has no egress to the database")
	}
	if pg := policy("noord-pg"); !ingressFrom(pg, 5432, "grafana", "") {
		t.Errorf("the database policy does not admit Grafana on 5432: %v", pg["spec"])
	}
}

// covers: spec 0016 AC-9, AC-11. The view only role is a managed role with
// five connections and no inheritance, created with its statement timeout on
// a fresh database, and Grafana's Ingress is the tailnet one.
func TestGrafanaRoleAndIngress(t *testing.T) {
	docs := renderPart(t, componentAgent)
	cluster := object(docs, "Cluster", "noord-pg")
	spec, _ := cluster["spec"].(map[string]any)
	managed, _ := spec["managed"].(map[string]any)
	var role map[string]any
	for _, r := range managed["roles"].([]any) {
		if rm, _ := r.(map[string]any); rm["name"] == "noord_grafana" {
			role = rm
		}
	}
	if role == nil || role["connectionLimit"] != 5 || role["inherit"] != false || role["login"] != true {
		t.Errorf("managed role noord_grafana = %v, want login, no inherit, connection limit 5", role)
	}
	init, _ := spec["bootstrap"].(map[string]any)["initdb"].(map[string]any)
	sql := strings.Join(toStrings(init["postInitSQL"]), "; ")
	if !strings.Contains(sql, "CREATE ROLE noord_grafana") || !strings.Contains(sql, "statement_timeout = '10s'") {
		t.Errorf("postInitSQL = %q, want the role with a 10s statement timeout", sql)
	}
	ing := object(docs, "Ingress", "grafana")
	ispec, _ := ing["spec"].(map[string]any)
	svc, _ := ispec["defaultBackend"].(map[string]any)["service"].(map[string]any)
	if ispec["ingressClassName"] != "tailscale" || svc["name"] != "grafana" || ispec["rules"] != nil {
		t.Errorf("Grafana's Ingress = %v, want tailscale to the grafana Service only", ispec)
	}
}

func toStrings(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, e := range list {
		s, _ := e.(string)
		out = append(out, s)
	}
	return out
}

// covers: spec 0016 AC-11. Grafana's login and database password come from
// sealed Secrets, never plain values; the agent part seals noord-grafana only
// with observability on, and noord-pg-grafana always (migration 00008 needs
// the role).
func TestGrafanaSecretsFollowObservability(t *testing.T) {
	on := renderPart(t, componentAgent)
	off := renderPart(t, componentAgent, "--set", "observability.enabled=false")
	if object(on, "SealedSecret", "noord-grafana") == nil || object(on, "SealedSecret", "noord-pg-grafana") == nil {
		t.Error("with observability on, the agent part lacks a sealed noord-grafana or noord-pg-grafana")
	}
	if object(off, "SealedSecret", "noord-grafana") != nil {
		t.Error("with observability off, noord-grafana is still sealed")
	}
	if object(off, "SealedSecret", "noord-pg-grafana") == nil {
		t.Error("with observability off, noord-pg-grafana is gone, but the role still has to exist")
	}
	for _, name := range []string{"noord-otel-collector", "noord-grafana-datasources", "noord-grafana-dashboards"} {
		if object(off, "ConfigMap", name) != nil {
			t.Errorf("with observability off, ConfigMap %s still renders", name)
		}
	}
	for _, kind := range []string{"Ingress"} {
		for _, d := range off {
			if d["kind"] == kind && nameOf(d) == "grafana" {
				t.Errorf("with observability off, the grafana %s still renders", kind)
			}
		}
	}
	if object(off, "NetworkPolicy", "grafana") != nil || object(off, "NetworkPolicy", "tempo") != nil {
		t.Error("with observability off, the observability NetworkPolicies still render")
	}
}

// covers: spec 0016 AC-12. The install renders and waits for the three
// observability Applications only with observability on, and Grafana's Secret
// follows the same switch.
func TestInstallerFollowsObservability(t *testing.T) {
	on := withObservability(components(true), true)
	if !slices.Equal(on, []string{componentAgent, componentDirectory, componentClient, componentCollector, componentTempo, componentGrafana}) {
		t.Errorf("components with observability = %v", on)
	}
	if got := withObservability(components(true), false); !slices.Equal(got, components(true)) {
		t.Errorf("components without observability = %v", got)
	}
	if base := components(true); len(base) != 3 {
		t.Errorf("withObservability changed the base list: %v", base)
	}
	tbl := realTable(t)
	if slices.Contains(tbl.Active(on, Conditions{Observability: false}), "noord-grafana") {
		t.Error("noord-grafana is active with observability off")
	}
	if !slices.Contains(tbl.Active(on, Conditions{Observability: true}), "noord-grafana") {
		t.Error("noord-grafana is not active with observability on")
	}
	if !slices.Contains(tbl.Active(on, Conditions{}), "noord-pg-grafana") {
		t.Error("noord-pg-grafana must exist whatever observability says")
	}
	var v Values
	if !v.ObservabilityEnabled() {
		t.Error("observability is off by default; the chart's default is on")
	}
	off := false
	v.Observability.Enabled = &off
	if v.ObservabilityEnabled() || v.Conditions().Observability {
		t.Error("observability.enabled: false was ignored")
	}
}
