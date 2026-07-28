{{/*
Expand the name of the chart.
*/}}
{{- define "provider-kserve.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "provider-kserve.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "provider-kserve.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "provider-kserve.labels" -}}
helm.sh/chart: {{ include "provider-kserve.chart" . }}
{{ include "provider-kserve.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "provider-kserve.selectorLabels" -}}
app.kubernetes.io/name: {{ include "provider-kserve.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "provider-kserve.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "provider-kserve.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Container image
*/}}
{{- define "provider-kserve.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end }}

{{/*
Names for the optional shared Envoy AI Gateway resources.
*/}}
{{- define "provider-kserve.aiGatewayName" -}}
{{- printf "%s-ai-gateway" (include "provider-kserve.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "provider-kserve.aiGatewayClassName" -}}
{{- printf "%s-ai-gateway" (include "provider-kserve.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "provider-kserve.aiGatewayReaderBindingName" -}}
{{- printf "%s-ai-gateway-pool-reader" (include "provider-kserve.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
