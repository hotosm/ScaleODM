{{/*
Expand the name of the chart.
*/}}
{{- define "scaleodm.name" -}}
{{- default .Chart.Name .Values.nameOverride | lower | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "scaleodm.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | lower | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride | lower }}
{{- $releaseName := .Release.Name | lower }}
{{- if contains $name $releaseName }}
{{- $releaseName | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" $releaseName $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "scaleodm.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "scaleodm.labels" -}}
helm.sh/chart: {{ include "scaleodm.chart" . }}
{{ include "scaleodm.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "scaleodm.selectorLabels" -}}
app.kubernetes.io/name: {{ include "scaleodm.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "scaleodm.serviceAccountName" -}}
{{- if .Values.kubernetes.serviceAccount.create }}
{{- default (include "scaleodm.fullname" .) .Values.kubernetes.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.kubernetes.serviceAccount.name }}
{{- end }}
{{- end }}



{{/*
The RustFS subchart's Service name. It mirrors the subchart's own fullname
logic and appends the "-svc" suffix the subchart uses - deriving it as
"<release>-rustfs" gives a name that does not resolve, and every S3 call fails
on DNS rather than anything that points at the cause.
*/}}
{{- define "scaleodm.rustfsServiceName" -}}
{{- $v := .Values.rustfs | default dict -}}
{{- $base := "" -}}
{{- if $v.fullnameOverride -}}
{{- $base = $v.fullnameOverride -}}
{{- else -}}
{{- $name := $v.nameOverride | default "rustfs" -}}
{{- if contains $name .Release.Name -}}
{{- $base = .Release.Name -}}
{{- else -}}
{{- $base = printf "%s-%s" .Release.Name $name -}}
{{- end -}}
{{- end -}}
{{- printf "%s-svc" ($base | trunc 59 | trimSuffix "-") -}}
{{- end }}

{{/* Host:port the API and workflow pods use to reach the in-cluster store. */}}
{{- define "scaleodm.rustfsHost" -}}
{{- .Values.s3.rustfs.endpoint | default (include "scaleodm.rustfsServiceName" .) -}}
{{- end }}

{{/*
rustfs.extraEnv repeats secrets.runtime's names, because Helm values cannot
reference each other. Left to drift, the store accepts credentials nothing else
was given, and the only symptom is a runtime 403.
*/}}
{{- define "scaleodm.validateRustfsCredentialMapping" -}}
{{- $secretName := .Values.secrets.runtime.name -}}
{{- $want := dict
      "RUSTFS_ACCESS_KEY" .Values.secrets.runtime.keys.accessKey
      "RUSTFS_SECRET_KEY" .Values.secrets.runtime.keys.secretKey -}}
{{- $extraEnv := dig "extraEnv" (list) (.Values.rustfs | default dict) -}}
{{- $mapped := 0 -}}
{{- range $name, $key := $want -}}
{{- range $entry := $extraEnv -}}
{{- if eq (dig "name" "" $entry) $name -}}
{{- $mapped = add1 $mapped -}}
{{- $ref := dig "valueFrom" "secretKeyRef" dict $entry -}}
{{- if or (ne (dig "name" "" $ref) $secretName) (ne (dig "key" "" $ref) $key) -}}
{{- fail (printf "rustfs.extraEnv %s reads secret %q key %q, but secrets.runtime gives the API %q key %q. Point them at the same credentials." $name (dig "name" "<none>" $ref) (dig "key" "<none>" $ref) $secretName $key) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if and (gt $mapped 0) (ne $mapped 2) -}}
{{- fail "rustfs.extraEnv maps only one of RUSTFS_ACCESS_KEY/RUSTFS_SECRET_KEY. Map both, or neither and put both in the secret named by rustfs.secret.existingSecret." -}}
{{- end -}}
{{- end }}
