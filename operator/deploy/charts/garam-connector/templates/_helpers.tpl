{{/*
The names every object shares. One release per namespace: the connector's Service, Secret and
ConfigMap names are its flags' defaults, which the manager's flags name too.
*/}}
{{- define "garam-connector.serviceName" -}}garam-connector{{- end -}}
{{- define "garam-connector.identitySecret" -}}garam-connector-identity{{- end -}}
{{- define "garam-connector.trustConfigMap" -}}garam-connector-trust{{- end -}}
{{- define "garam-connector.naruCallerSecret" -}}garam-connector-naru-caller{{- end -}}
{{- define "garam-connector.claimName" -}}garam-connector-claim{{- end -}}
{{- define "garam-connector.audience" -}}garam-connector{{- end -}}
{{- define "garam-connector.botTokenPort" -}}8443{{- end -}}
{{- define "garam-connector.workspaceAudience" -}}garam-workspace{{- end -}}

{{/* The release a claim names, <release-namespace>/<release-name> (CLAIM-1). */}}
{{- define "garam-connector.release" -}}{{ .Release.Namespace }}/{{ .Release.Name }}{{- end -}}

{{/* The watched namespaces, as a list: the release namespace alone where none is given. */}}
{{- define "garam-connector.watched" -}}
{{- $watched := .Values.watchNamespaces | default (list .Release.Namespace) | uniq -}}
{{- toJson $watched -}}
{{- end -}}

{{- define "garam-connector.labels" -}}
app.kubernetes.io/part-of: garam-connector
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/* The selector of one program's Pods; the argument is a dict of root and program. */}}
{{- define "garam-connector.selector" -}}
app.kubernetes.io/name: {{ .program }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end -}}

{{/* image refuses a reference not pinned by digest; the argument is a dict of name and ref. */}}
{{- define "garam-connector.image" -}}
{{- if not (regexMatch "^[a-z0-9]([a-z0-9._/:-]*[a-z0-9])?:[A-Za-z0-9_][A-Za-z0-9._-]*@sha256:[0-9a-f]{64}$" .ref) -}}
{{- fail (printf "images.%s must be <repository>:<tag>@sha256:<digest>, and is %q" .name .ref) -}}
{{- end -}}
{{- .ref -}}
{{- end -}}

{{/* The Pod-wide security context PodSecurity restricted admits (ADR 0091 decision 7). */}}
{{- define "garam-connector.podSecurity" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "garam-connector.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end -}}

{{/* The DNS egress rule. */}}
{{- define "garam-connector.dnsEgress" -}}
- to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: {{ .Values.dns.namespace }}
      podSelector:
        matchLabels:
          {{- toYaml .Values.dns.podSelector | nindent 10 }}
  ports:
    - protocol: UDP
      port: 53
    - protocol: TCP
      port: 53
{{- end -}}

{{/* The API server egress rule; apiServer.cidr is required. */}}
{{- define "garam-connector.apiServerEgress" -}}
- to:
    - ipBlock:
        cidr: {{ required "apiServer.cidr is required: the address range the Kubernetes API is reached at" .Values.apiServer.cidr }}
  ports:
    - protocol: TCP
      port: {{ .Values.apiServer.port }}
{{- end -}}

{{/* The metrics ingress rule, where metrics are on. */}}
{{- define "garam-connector.metricsIngress" -}}
{{- if .Values.metrics.enabled }}
{{- if not .Values.metrics.from }}
{{- fail "metrics.from is required where metrics are enabled: name who scrapes them" }}
{{- end }}
- from:
    {{- toYaml .Values.metrics.from | nindent 4 }}
  ports:
    - protocol: TCP
      port: {{ .Values.metrics.port }}
{{- end }}
{{- end -}}


{{/*
garamOn answers "true" where the connector connects to garam: connector.garamURL and
connector.garamRoot.configMap are set together. Neither set, garam is off: the connector registers
nothing and reaches no garam (ADR 0099 decision 3), so a proxy to garam is refused.
*/}}
{{- define "garam-connector.garamOn" -}}
{{- $c := .Values.connector -}}
{{- if and $c.garamURL $c.garamRoot.configMap -}}
true
{{- else if or $c.garamURL $c.garamRoot.configMap -}}
{{- fail "connector.garamURL and connector.garamRoot.configMap are set together, or neither is and garam is off" -}}
{{- else if .Values.proxy.url -}}
{{- fail "proxy.url is set where garam is off: the proxy is how the connector reaches garam" -}}
{{- end -}}
{{- end -}}

{{/*
workspaceEgress renders a workspaces.<provider>.egress list as the connector's
<cidr>/<protocol>/<port>[-<port>] rules, and refuses one without a rule to port 53, so a workspace
resolves its URLs' names (ADR 0099 decision 7). The argument is a dict of name and rules.
*/}}
{{- define "garam-connector.workspaceEgress" -}}
{{- $rules := list -}}
{{- $dns := false -}}
{{- range .rules -}}
{{- $port := required (printf "workspaces.%s.egress: each rule names a port" $.name) .port | int -}}
{{- $end := .endPort | default $port | int -}}
{{- if and (le $port 53) (ge $end 53) -}}{{- $dns = true -}}{{- end -}}
{{- $range := ternary (printf "%d" $port) (printf "%d-%d" $port $end) (eq $port $end) -}}
{{- $rules = append $rules (printf "%s/%s/%s" (required (printf "workspaces.%s.egress: each rule names a cidr" $.name) .cidr) (required (printf "workspaces.%s.egress: each rule names a protocol" $.name) .protocol) $range) -}}
{{- end -}}
{{- if not $dns -}}
{{- fail (printf "workspaces.%s.egress must include a rule to port 53, so a workspace resolves its URLs' names" .name) -}}
{{- end -}}
{{- join "," $rules -}}
{{- end -}}

{{/*
workspacesOn answers "true" where the connector serves the bots' workspaces, and refuses a partial
configuration as the connector does, so a render does not ship one it would refuse at start.
*/}}
{{- define "garam-connector.workspacesOn" -}}
{{- $w := .Values.workspaces -}}
{{- if and .Values.connector.enabled $w.namespace -}}
{{- $watched := include "garam-connector.watched" . | fromJsonArray -}}
{{- if has $w.namespace (append $watched .Release.Namespace) -}}
{{- fail (printf "workspaces.namespace %s is the release namespace or a watched one" $w.namespace) -}}
{{- end -}}
{{- if not .Values.connector.naru.enabled -}}
{{- if or $w.naru.modelGatewayURL $w.naru.toolProxyURL $w.naru.egress -}}
{{- fail "workspaces.naru is set where connector.naru is off" -}}
{{- end -}}
{{- end -}}
true
{{- else if and .Values.connector.enabled (or .Values.images.workspaceHelper .Values.images.agents $w.pod.modelGatewayURL $w.pod.toolProxyURL $w.pod.egress $w.naru.modelGatewayURL $w.naru.toolProxyURL $w.naru.egress) -}}
{{- fail "images.workspaceHelper, images.agents or workspaces.pod or naru is set without workspaces.namespace" -}}
{{- end -}}
{{- end -}}

{{/*
mailboxOn answers "true" where a garam-owned Bot long-polls its garam mailbox: connector.garamMachine.url
and .origin are set together, with garam and the connector on. One without the other, or either with
garam or the connector off, is refused.
*/}}
{{- define "garam-connector.mailboxOn" -}}
{{- $m := .Values.connector.garamMachine -}}
{{- if and $m.url $m.origin -}}
{{- if not .Values.connector.enabled -}}
{{- fail "connector.garamMachine is set where the connector is off: a Bot reaches no garam token without it" -}}
{{- else if not (include "garam-connector.garamOn" .) -}}
{{- fail "connector.garamMachine is set where garam is off" -}}
{{- end -}}
true
{{- else if or $m.url $m.origin -}}
{{- fail "connector.garamMachine.url and connector.garamMachine.origin are set together, or neither is" -}}
{{- end -}}
{{- end -}}

{{/*
botNoProxy is what a garam-owned Bot's Pod reaches directly where it reaches garam through proxy.url:
the connector's Service and the cluster's own names.
*/}}
{{- define "garam-connector.botNoProxy" -}}
{{ include "garam-connector.serviceName" . }}.{{ .Release.Namespace }}.svc,.svc,.cluster.local,localhost,127.0.0.1
{{- end -}}
