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
