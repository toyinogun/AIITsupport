{{/* The Secret table shared with the installer (files/secrets.yaml). */}}
{{- define "noord.table" -}}
{{- .Files.Get "files/secrets.yaml" -}}
{{- end -}}

{{/* image: the full reference for one of our images; pass (list $ "agent"). */}}
{{- define "noord.image" -}}
{{- $root := index . 0 -}}
{{- $name := index . 1 -}}
{{- $img := $root.Values.image -}}
{{- printf "%s/%s/aiitsupport-%s:%s" $img.registry (required "image.owner is required" $img.owner) $name (required "image.tag is required (a full git SHA)" $img.tag) -}}
{{- end -}}

{{- define "noord.labels" -}}
app.kubernetes.io/part-of: noord
app.kubernetes.io/managed-by: argocd
noord.example/cluster: {{ .Values.cluster.name | quote }}
{{- end -}}

{{/*
sealedSecrets: one SealedSecret per table entry of this component, from
.Values.sealed (a backup Secret only with backup.enabled, the Tailscale one only with platform.tailscale.enabled). Fails when a
required key (for this demo and reset setting) is missing.
*/}}
{{- define "noord.sealedSecrets" -}}
{{- $root := . -}}
{{- $table := include "noord.table" . | fromYaml -}}
{{- range $name, $s := $table.secrets }}
{{- if and (eq $s.component $root.Values.component) (or (not $s.backup) $root.Values.backup.enabled) (or (not $s.tailscale) $root.Values.platform.tailscale.enabled) }}
{{- $data := index $root.Values.sealed $name | default dict }}
{{- range $key, $k := $s.keys }}
{{- $applies := and (or (not (hasKey $k "demo")) (eq $k.demo $root.Values.demo.enabled)) (or (not (hasKey $k "reset")) (eq $k.reset $root.Values.reset.enabled)) }}
{{- if and $applies (not $k.optional) (not (hasKey $data $key)) }}
{{- fail (printf "sealed-values.yaml has no %s in %s; run make install or make seal" $key $name) }}
{{- end }}
{{- end }}
---
apiVersion: bitnami.com/v1alpha1
kind: SealedSecret
metadata:
  name: {{ $name }}
  namespace: {{ $s.namespace }}
  labels:
    {{- include "noord.labels" $root | nindent 4 }}
  annotations:
    argocd.argoproj.io/sync-wave: "0"
spec:
  encryptedData:
    {{- toYaml $data | nindent 4 }}
  template:
    metadata:
      name: {{ $name }}
      namespace: {{ $s.namespace }}
      labels:
        {{- include "noord.labels" $root | nindent 8 }}
    type: {{ $s.type | default "Opaque" }}
{{- end }}
{{- end }}
{{- end -}}

{{/*
secretEnv: one secretKeyRef env entry per key of a Secret that its sealed
block holds. Mounted keys (fromFile, like the Graph certificate) are left
out, so a key never lands in the environment. Pass (list $ "noord-toolserver").
*/}}
{{- define "noord.secretEnv" -}}
{{- $root := index . 0 -}}
{{- $name := index . 1 -}}
{{- $table := include "noord.table" $root | fromYaml -}}
{{- $data := index $root.Values.sealed $name | default dict -}}
{{- range $key, $k := (index $table.secrets $name).keys }}
{{- if and (hasKey $data $key) (not $k.fromFile) }}
- name: {{ $key }}
  valueFrom:
    secretKeyRef:
      name: {{ $name }}
      key: {{ $key }}
{{- end }}
{{- end }}
{{- end -}}

{{/* checksum of one Secret's sealed block, so a reseal rolls only its readers (AC-8). */}}
{{- define "noord.sealedChecksum" -}}
{{- $root := index . 0 -}}
{{- $name := index . 1 -}}
{{- index $root.Values.sealed $name | default dict | toYaml | sha256sum -}}
{{- end -}}

{{/* Pod level security for the restricted Pod Security level (AC-12). */}}
{{- define "noord.podSecurity" -}}
automountServiceAccountToken: false
securityContext:
  runAsNonRoot: true
  runAsUser: 65532
  runAsGroup: 65532
  fsGroup: 65532
  seccompProfile:
    type: RuntimeDefault
{{- end -}}

{{- define "noord.containerSecurity" -}}
securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop: ["ALL"]
{{- end -}}

{{/* The CNPG CA volume every database client mounts (sslmode=verify-full). */}}
{{- define "noord.pgCAVolume" -}}
- name: pg-ca
  secret:
    secretName: noord-pg-ca
    items:
      - key: ca.crt
        path: ca.crt
{{- end -}}

{{- define "noord.pgCAMount" -}}
{{- $table := include "noord.table" . | fromYaml -}}
- name: pg-ca
  mountPath: {{ dir $table.database.caPath }}
  readOnly: true
{{- end -}}

{{/* env: a map of setting name to value, as an env list. */}}
{{- define "noord.env" -}}
{{- range $k, $v := . -}}
- name: {{ $k }}
  value: {{ $v | toString | quote }}
{{ end -}}
{{- end -}}

{{/*
zammadRailsEnv: the database, Redis, and Memcached environment of the Zammad
chart's Rails pods, from its own zammad.env template and the same values,
with the two passwords read from the helpdesk Job's own Secret (AC-11). A
test compares its names with the rendered railsserver container's.
*/}}
{{- define "noord.zammadRailsEnv" -}}
{{- $own := dict "secrets" (dict
  "postgresql" (dict "useExisting" true "secretName" "noord-seed-helpdesk" "secretKey" "ZAMMAD_POSTGRES_PASSWORD")
  "redis" (dict "useExisting" true "secretName" "noord-seed-helpdesk" "secretKey" "ZAMMAD_REDIS_PASSWORD")) -}}
{{- $values := merge $own (deepCopy .Values.zammad) -}}
{{- $ctx := dict "Values" $values "Release" .Release -}}
{{- include "zammad.env" $ctx }}
{{ include "zammad.env.failOnPendingMigrations" $ctx -}}
{{- end -}}
