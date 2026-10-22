{{/* Pass the root context, or (dict "root" $ "createNamespace" true). */}}
{{- define "apps.syncPolicy" -}}
{{- $createNamespace := false -}}
{{- $root := . -}}
{{- if hasKey . "root" }}{{ $root = .root }}{{ $createNamespace = .createNamespace }}{{ end -}}
syncPolicy:
  automated:
    prune: true
    selfHeal: true
  syncOptions:
    - ServerSideApply=true
    - SkipDryRunOnMissingResource=true
    - RespectIgnoreDifferences=true
    {{- if $createNamespace }}
    - CreateNamespace=true
    {{- end }}
  retry:
    limit: {{ $root.Values.sync.retryLimit }}
    backoff:
      duration: 5s
      factor: 2
      maxDuration: {{ $root.Values.sync.maxBackoff }}
{{- end -}}

{{- define "apps.labels" -}}
app.kubernetes.io/part-of: noord-{{ required "cluster.name is required" .Values.cluster.name }}
{{- end -}}

{{/* One of our component Applications; pass (dict "root" $ "component" "agent" "namespace" "noord-agent" "wave" "0"), plus optional "values" for its valuesObject. */}}
{{- define "apps.component" -}}
{{- $root := .root -}}
{{- $c := $root.Values.cluster.name -}}
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: noord-{{ .component }}
  namespace: argocd
  labels:
    {{- include "apps.labels" $root | nindent 4 }}
  annotations:
    argocd.argoproj.io/sync-wave: {{ .wave | quote }}
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: noord
  source:
    repoURL: {{ required "repo.url is required" $root.Values.repo.url }}
    targetRevision: {{ $root.Values.repo.revision }}
    path: deploy/helm/noord
    helm:
      # The Zammad chart's Helm test Jobs are not part of the install.
      skipTests: true
      valueFiles:
        - values.yaml
        - ../../clusters/{{ $c }}/values.yaml
        - ../../clusters/{{ $c }}/sealed-values.yaml
      valuesObject:
        component: {{ .component }}
        {{- with .values }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
  destination:
    server: https://kubernetes.default.svc
    namespace: {{ .namespace }}
  {{- with .ignoreDifferences }}
  ignoreDifferences:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- include "apps.syncPolicy" $root | nindent 2 }}
{{- end -}}

{{- /* One operator's Application: pass (dict "root" $ "key" "certManager" "app" "cert-manager" "wave" "-30" "namespace" "cert-manager" "values" "<yaml>"). */}}
{{- define "platform.operator" -}}
{{- $root := .root -}}
{{- $v := index $root.Values.platform .key -}}
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: noord-{{ $root.Values.cluster.name }}-{{ .app }}
  namespace: argocd
  labels:
    {{- include "apps.labels" $root | nindent 4 }}
  annotations:
    argocd.argoproj.io/sync-wave: {{ .wave | quote }}
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: noord-platform
  source:
    repoURL: {{ $v.repoURL }}
    chart: {{ $v.chart }}
    targetRevision: {{ $v.version }}
    {{- with .values }}
    helm:
      valuesObject:
        {{- . | nindent 8 }}
    {{- end }}
  destination:
    server: https://kubernetes.default.svc
    namespace: {{ .namespace }}
  {{- include "apps.syncPolicy" (dict "root" $root "createNamespace" true) | nindent 2 }}
{{- end -}}
