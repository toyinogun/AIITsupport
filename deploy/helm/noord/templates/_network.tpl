{{/*
The network allow list (AC-14). Every namespace gets noord.netBase (default
deny both ways, plus DNS to kube-dns); each workload then gets exactly the
allows the spec lists, in its namespace's network.yaml.
*/}}

{{/* netBase: the default deny and the DNS allow of one namespace. Pass (list $ "<namespace>"). */}}
{{- define "noord.netBase" -}}
{{- $root := index . 0 }}
{{- $ns := index . 1 }}
{{- if and $root.Values.cilium.fqdnEgress (not $root.Values.cilium.enabled) }}
{{- fail "cilium.fqdnEgress needs cilium.enabled (the cluster's CNI is Cilium)" }}
{{- end }}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny
  namespace: {{ $ns }}
  labels:
    {{- include "noord.labels" $root | nindent 4 }}
  annotations:
    argocd.argoproj.io/sync-wave: "0"
spec:
  podSelector: {}
  policyTypes: [Ingress, Egress]
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: dns
  namespace: {{ $ns }}
  labels:
    {{- include "noord.labels" $root | nindent 4 }}
  annotations:
    argocd.argoproj.io/sync-wave: "0"
spec:
  podSelector: {}
  policyTypes: [Egress]
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              k8s-app: kube-dns
      ports:
        - {protocol: UDP, port: 53}
        - {protocol: TCP, port: 53}
{{- end -}}

{{/*
egressHTTPS: TCP 443 out of the cluster for one workload. Without Cilium
FQDN rules it is any address outside the pod, service, and node ranges;
with them (cilium.fqdnEgress) it is only the named hosts, plus the DNS
rule FQDN matching needs. Only one of the two renders, because allows add
up: a Cilium policy cannot narrow a NetworkPolicy that already allows more.
Pass (list $ "<namespace>" "<app.kubernetes.io/name>" (list <fqdn patterns>)),
plus a matchLabels dict when the pods are not selected by that name (the
database's).
*/}}
{{- define "noord.egressHTTPS" -}}
{{- $root := index . 0 }}
{{- $ns := index . 1 }}
{{- $name := index . 2 }}
{{- $hosts := index . 3 }}
{{- $labels := dict "app.kubernetes.io/name" $name }}
{{- if gt (len .) 4 }}{{ $labels = index . 4 }}{{ end }}
{{- $c := $root.Values.cluster }}
{{- if $root.Values.cilium.fqdnEgress }}
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: {{ $name }}-https
  namespace: {{ $ns }}
  labels:
    {{- include "noord.labels" $root | nindent 4 }}
  annotations:
    argocd.argoproj.io/sync-wave: "0"
spec:
  endpointSelector:
    matchLabels:
      {{- toYaml $labels | nindent 6 }}
  egress:
    # Cilium learns the names' addresses from the DNS answers it proxies.
    - toEndpoints:
        - matchLabels:
            k8s:io.kubernetes.pod.namespace: kube-system
            k8s-app: kube-dns
      toPorts:
        - ports:
            - {port: "53", protocol: ANY}
          rules:
            dns:
              - matchPattern: "*"
    - toFQDNs:
        {{- range $hosts }}
        {{- if contains "*" . }}
        - matchPattern: {{ . | quote }}
        {{- else }}
        - matchName: {{ . | quote }}
        {{- end }}
        {{- end }}
      toPorts:
        - ports:
            - {port: "443", protocol: TCP}
{{- else }}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ $name }}-https
  namespace: {{ $ns }}
  labels:
    {{- include "noord.labels" $root | nindent 4 }}
  annotations:
    argocd.argoproj.io/sync-wave: "0"
spec:
  podSelector:
    matchLabels:
      {{- toYaml $labels | nindent 6 }}
  policyTypes: [Egress]
  egress:
    - to:
        - ipBlock:
            cidr: 0.0.0.0/0
            except:
              - {{ required "cluster.podCIDR is required" $c.podCIDR }}
              - {{ required "cluster.serviceCIDR is required" $c.serviceCIDR }}
              - {{ required "cluster.nodeCIDR is required (make cluster-init fills it)" $c.nodeCIDR }}
      ports:
        - {protocol: TCP, port: 443}
{{- end }}
{{- end -}}

{{/* tailscaleProxy: the Tailscale operator's proxy pods for one of our Ingresses. Pass (list $ "<ingress namespace>" "<ingress name>"). */}}
{{- define "noord.tailscaleProxy" -}}
{{- $root := index . 0 }}
- namespaceSelector:
    matchLabels:
      kubernetes.io/metadata.name: {{ $root.Values.tailscale.proxyNamespace }}
  podSelector:
    matchLabels:
      tailscale.com/parent-resource-type: ingress
      tailscale.com/parent-resource-ns: {{ index . 1 }}
      tailscale.com/parent-resource: {{ index . 2 }}
{{- end -}}

{{/* zammadPods: every pod of the Zammad chart (its release is the noord-client Application). */}}
{{- define "noord.zammadPods" -}}
matchLabels:
  app.kubernetes.io/instance: noord-client
{{- end -}}

{{/* Hosts the agent's Slack and model clients reach (AC-14). */}}
{{- define "noord.agentHosts" -}}
{{- list "slack.com" "*.slack.com" "*.slack-edge.com" "api.anthropic.com" | toJson -}}
{{- end -}}

{{/* Hosts Microsoft Graph and its token endpoint live on (AC-14). */}}
{{- define "noord.graphHosts" -}}
{{- list "graph.microsoft.com" "login.microsoftonline.com" | toJson -}}
{{- end -}}
