{{- define "apps.syncPolicy" -}}
syncPolicy:
  automated:
    prune: true
    selfHeal: true
  syncOptions:
    - ServerSideApply=true
    - SkipDryRunOnMissingResource=true
    - RespectIgnoreDifferences=true
  retry:
    limit: {{ .Values.sync.retryLimit }}
    backoff:
      duration: 5s
      factor: 2
      maxDuration: {{ .Values.sync.maxBackoff }}
{{- end -}}

{{- define "apps.labels" -}}
app.kubernetes.io/part-of: noord-{{ required "cluster.name is required" .Values.cluster.name }}
{{- end -}}

{{/* One of our component Applications; pass (dict "root" $ "component" "agent" "namespace" "noord-agent" "wave" "0"). */}}
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
      valueFiles:
        - values.yaml
        - ../../clusters/{{ $c }}/values.yaml
        - ../../clusters/{{ $c }}/sealed-values.yaml
      valuesObject:
        component: {{ .component }}
  destination:
    server: https://kubernetes.default.svc
    namespace: {{ .namespace }}
  {{- with .ignoreDifferences }}
  ignoreDifferences:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- include "apps.syncPolicy" $root | nindent 2 }}
{{- end -}}
