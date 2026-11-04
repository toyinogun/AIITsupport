#!/usr/bin/env bash
# Deploy checks (spec 0015, AC-26): lint both charts, render every cluster
# folder's parts, validate the output against the Kubernetes and CRD schemas,
# and fail when any rendered Secret carries data. Needs helm; kubeconform runs
# through go tool. Run it with `make deploy-check`.
set -euo pipefail

cd "$(dirname "$0")/.."

# CRD schemas for SealedSecret, CNPG, Barman Cloud, Cilium, and ArgoCD.
CRD_SCHEMAS='https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'

kubeconform() {
  go tool -modfile=tools.mod kubeconform -strict -summary \
    -schema-location default -schema-location "$CRD_SCHEMAS" "$@"
}

# no_plain_secret fails when a rendered Secret holds data or stringData.
no_plain_secret() {
  local file=$1 label=$2
  if awk -v RS='\n---\n' '/(^|\n)kind: Secret(\n|$)/ && /(^|\n)(string)?[Dd]ata:/ { found = 1 } END { exit !found }' "$file"; then
    echo "::error::$label renders a Secret with data" >&2
    return 1
  fi
}

# check_render validates one rendered file and keeps the secret check.
check_render() {
  local file=$1 label=$2
  echo "== $label"
  kubeconform "$file"
  no_plain_secret "$file" "$label"
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# render_part renders one component into $out. A cluster's sealed-values.yaml
# lacks a key the chart newly needs until `make install` seals it (the chart
# refuses to render then, on purpose). The check must still validate the
# rest, so it stands in for each key the chart names as missing and warns,
# and any other render error still fails. Pass the args after `helm template`.
render_part() {
  local out=$1; shift
  local err="$work/render.err" extra=() msg secret key
  for _ in $(seq 1 20); do
    if helm template "$@" "${extra[@]}" >"$out" 2>"$err"; then
      return 0
    fi
    msg=$(grep -o 'sealed-values.yaml has no [^ ]* in [^;]*' "$err" | head -n1 || true)
    if [ -z "$msg" ]; then
      cat "$err" >&2
      return 1
    fi
    key=$(awk '{print $4}' <<<"$msg")
    secret=$(awk '{print $6}' <<<"$msg")
    echo "::warning::${secret} lacks ${key} in sealed-values.yaml; rendering with a stand in (make install seals it)" >&2
    extra+=(--set "sealed.${secret}.${key//./\\.}=AgB-stand-in")
  done
  echo "::error::too many missing sealed keys" >&2
  return 1
}

helm lint deploy/argocd deploy/helm/noord

for dir in deploy/clusters/*/; do
  cluster=$(basename "$dir")
  values="$dir/values.yaml"
  [ -f "$values" ] || continue

  for part in platform apps; do
    out="$work/$cluster-$part.yaml"
    helm template "noord-apps" deploy/argocd -f deploy/argocd/values.yaml -f "$values" --set "part=$part" >"$out"
    check_render "$out" "$cluster bootstrap $part"
  done

  # A folder with no sealed values yet (the template) has nothing to render.
  if ! grep -Eq '^sealed: *\{\} *$' "$dir/sealed-values.yaml" 2>/dev/null; then
    for comp in agent directory client; do
      args=(--set "component=$comp")
      [ "$comp" = client ] && args+=(--set zammad.enabled=true)
      # directory and client exist only on a demo cluster.
      if [ "$comp" != agent ] && ! grep -q "name: noord-$comp\$" "$work/$cluster-apps.yaml"; then
        continue
      fi
      out="$work/$cluster-$comp.yaml"
      render_part "$out" "noord-$comp" deploy/helm/noord --skip-tests -f "$values" -f "$dir/sealed-values.yaml" "${args[@]}"
      check_render "$out" "$cluster $comp"
    done
  fi
done

# The drift tests (AC-7, AC-11) and the chart tests skip without helm in the
# main test job, so run them here where helm exists.
go test -race ./internal/install/
