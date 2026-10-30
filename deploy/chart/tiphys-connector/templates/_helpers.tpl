{{- define "tiphys.labels" -}}
app.kubernetes.io/name: tiphys-connector
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* The identity every portal call is made as; also the admission match. */}}
{{- define "tiphys.user" -}}tiphys:user{{- end -}}
{{- define "tiphys.group" -}}tiphys:users{{- end -}}

{{/* Fails the render when debug is on without what makes it safe. */}}
{{- define "tiphys.debugChecked" -}}
{{- if .Values.debug.enabled -}}
{{- if ne .Values.admission.mode "vap" -}}
{{- fail "debug.enabled needs admission.mode vap: without the tiphys-debug-only policy, adding ephemeral containers would expose Secrets and privileged containers" -}}
{{- end -}}
{{- if not (regexMatch "^[^'\"\\\\]+@sha256:[0-9a-f]{64}$" .Values.debug.image) -}}
{{- fail "debug.image must be pinned by digest (repo@sha256:<64 hex>)" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Kyverno deny condition: true when a JMESPath expression gives a different
result for request.object and request.oldObject. The expression takes the
object as %[1]s. Built with printf so Helm does not read Kyverno's braces.
*/}}
{{- define "tiphys.kyvernoChanged" -}}
- key: {{ printf "{{ to_string(%s) }}" (printf . "request.object") | quote }}
  operator: NotEquals
  value: {{ printf "{{ to_string(%s) }}" (printf . "request.oldObject") | quote }}
{{- end -}}
