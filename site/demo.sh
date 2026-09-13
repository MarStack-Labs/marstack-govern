#!/usr/bin/env bash
set -uo pipefail

PROMPT="${PROMPT:-\$ }"
SPEED="${SPEED:-0.028}"
PAUSE="${PAUSE:-1.4}"
CONTEXT="${GOVERN_DEMO_CONTEXT:-kind-margov}"
DIVISION="${GOVERN_DEMO_DIVISION:-payments}"

k() { kubectl --context "$CONTEXT" "$@"; }

type_out() {
  local text="$1" i
  printf '%s' "$PROMPT"
  for ((i = 0; i < ${#text}; i++)); do
    printf '%s' "${text:i:1}"
    sleep "$SPEED"
  done
  printf '\n'
}

step() {
  local shown="$*"
  type_out "kubectl ${shown#k }"
  sleep 0.4
  "$@"
  printf '\n'
  sleep "$PAUSE"
}

note() {
  printf '\033[2m# %s\033[0m\n\n' "$*"
  sleep 1.1
}

cleanup() {
  k delete quotarequest -n "$DIVISION-dev" demo-quota >/dev/null 2>&1
  k delete ephemeralenvironment "$DIVISION-991" >/dev/null 2>&1
}
trap cleanup EXIT

if ! command -v kubectl >/dev/null 2>&1; then
  echo "kubectl is not on PATH" >&2
  exit 1
fi
if ! k get crd divisions.govern.marstack.io >/dev/null 2>&1; then
  echo "context $CONTEXT has no Division CRD; is margov installed?" >&2
  exit 1
fi

cleanup
clear

note "A Division is reconciled into namespaces, quota and isolation."
step k get division

step k get ns -l govern.marstack.io/division="$DIVISION"

note "Nothing here was typed. The quota backend was chosen by looking at the cluster."
step k get division "$DIVISION" \
  -o jsonpath='{.status.quotaBackend}{"\n"}{range .status.conditions[?(@.type=="QuotaReady")]}{.reason}: {.message}{"\n"}{end}'

note "Now file a quota request, and claim to be someone else while doing it."
printf '\033[2m# spec.requestedBy: head-of-platform@marstack.test\033[0m\n\n'
sleep 1

type_out "kubectl apply -f quota-request.yaml --as intern@marstack.test"
sleep 0.5
k apply -f - --as intern@marstack.test --as-group system:masters <<YAML
apiVersion: govern.marstack.io/v1alpha1
kind: QuotaRequest
metadata:
  name: demo-quota
  namespace: $DIVISION-dev
spec:
  division: $DIVISION
  target:
    cpu: "64"
    memory: 128Gi
  reason: the p95 has been above the ceiling for a fortnight
  requestedBy: head-of-platform@marstack.test
YAML
printf '\n'
sleep "$PAUSE"

note "The manifest said head-of-platform. The API server wrote who actually sent it."
step k get quotarequest -n "$DIVISION-dev" demo-quota \
  -o jsonpath='{.spec.requestedBy}{"\n"}'

note "Wait for the controller to gather evidence."
type_out "kubectl wait --for=jsonpath='{.status.phase}'=AwaitingDecision ..."
k wait --for=jsonpath='{.status.phase}'=AwaitingDecision \
  quotarequest/demo-quota -n "$DIVISION-dev" --timeout=60s
printf '\n'
sleep "$PAUSE"

note "Preflight ran through the real admission chain. The simulation packed real nodes."
step k get quotarequest -n "$DIVISION-dev" demo-quota \
  -o jsonpath='{range .status.conditions[*]}{.type}={.reason}{"\n"}  {.message}{"\n\n"}{end}'

note "Nothing was approved. The request now carries the evidence an approver will see."
step k get quotarequest -n "$DIVISION-dev" demo-quota
