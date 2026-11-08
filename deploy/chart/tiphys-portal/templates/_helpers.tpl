{{- define "tiphys-portal.name" -}}tiphys-portal{{- end -}}

{{- define "tiphys-portal.selectorLabels" -}}
app.kubernetes.io/name: tiphys-portal
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "tiphys-portal.labels" -}}
{{ include "tiphys-portal.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "tiphys-portal.image" -}}
{{- if .Values.image.digest -}}
{{ .Values.image.repository }}@{{ .Values.image.digest }}
{{- else -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}

{{- define "tiphys-portal.publicUrl" -}}
{{- required "env.PUBLIC_URL is required (the portal's external URL)" .Values.env.PUBLIC_URL -}}
{{- end -}}
