{{/*
Name helpers follow Helm conventions so multiple releases can share a namespace.
*/}}
{{- define "dogit.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dogit.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "dogit.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dogit.labels" -}}
helm.sh/chart: {{ include "dogit.chart" . }}
{{ include "dogit.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "dogit.selectorLabels" -}}
app.kubernetes.io/name: {{ include "dogit.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "dogit.appSecretName" -}}
{{- if .Values.app.existingSecret -}}
{{- .Values.app.existingSecret -}}
{{- else -}}
{{- printf "%s-app-secret" (include "dogit.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "dogit.postgresqlSecretName" -}}
{{- if .Values.postgresql.auth.existingSecret -}}
{{- .Values.postgresql.auth.existingSecret -}}
{{- else -}}
{{- printf "%s-postgresql-secret" (include "dogit.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "dogit.postgresqlServiceName" -}}
{{- printf "%s-postgresql" (include "dogit.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dogit.publicHost" -}}
{{- if .Values.app.publicHost -}}
{{- .Values.app.publicHost -}}
{{- else if .Values.ingress.host -}}
{{- .Values.ingress.host -}}
{{- else -}}
localhost
{{- end -}}
{{- end -}}

{{/*
The database URL is always held in a Secret when the database is external. For an
in-cluster database, only the password is secret; Kubernetes expands the earlier env
var in DOGIT_DATABASE_URL when it starts the container.
*/}}
{{- define "dogit.databaseEnv" -}}
{{- if .Values.postgresql.enabled }}
- name: DOGIT_DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "dogit.postgresqlSecretName" . }}
      key: {{ .Values.postgresql.auth.passwordKey | quote }}
- name: DOGIT_DATABASE_URL
  value: {{ printf "postgres://%s:$(DOGIT_DB_PASSWORD)@%s:%v/%s?sslmode=disable" .Values.postgresql.auth.username (include "dogit.postgresqlServiceName" .) 5432 .Values.postgresql.auth.database | quote }}
{{- else }}
- name: DOGIT_DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ required "database.existingSecret must name a Secret containing the database DSN when postgresql.enabled=false" .Values.database.existingSecret }}
      key: {{ .Values.database.existingSecretKey | quote }}
{{- end }}
{{- end -}}

{{- define "dogit.allowedOrigins" -}}
{{- if .Values.app.allowedOrigins -}}
{{- join "," .Values.app.allowedOrigins -}}
{{- else if .Values.ingress.host -}}
{{- $scheme := "http" -}}
{{- if .Values.ingress.tls.enabled -}}{{- $scheme = "https" -}}{{- end -}}
{{- $origins := list (printf "%s://%s" $scheme .Values.ingress.host) -}}
{{- range .Values.ingress.additionalHosts -}}
{{- $origins = append $origins (printf "%s://%s" $scheme .) -}}
{{- end -}}
{{- join "," $origins -}}
{{- end -}}
{{- end -}}

{{- define "dogit.objectStorageEnv" -}}
{{- if .Values.objectStorage.enabled }}
- name: DOGIT_S3_ENDPOINT
  value: {{ required "objectStorage.endpoint is required when objectStorage.enabled=true" .Values.objectStorage.endpoint | quote }}
- name: DOGIT_S3_BUCKET
  value: {{ required "objectStorage.bucket is required when objectStorage.enabled=true" .Values.objectStorage.bucket | quote }}
- name: DOGIT_S3_REGION
  value: {{ .Values.objectStorage.region | quote }}
- name: DOGIT_OBJECTS_PREFIX
  value: {{ .Values.objectStorage.prefix | quote }}
- name: DOGIT_S3_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ required "objectStorage.existingSecret is required when objectStorage.enabled=true" .Values.objectStorage.existingSecret }}
      key: {{ .Values.objectStorage.accessKeyKey | quote }}
- name: DOGIT_S3_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.objectStorage.existingSecret }}
      key: {{ .Values.objectStorage.secretKeyKey | quote }}
{{- end }}
{{- end -}}
