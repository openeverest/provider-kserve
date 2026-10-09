{{/*
A best-effort uninstall hook: ServiceAccount, ClusterRole with .rules, binding
and a Job running .script with kubectl. Failures never block the uninstall.
*/}}
{{- define "provider-kserve.cleanupHook" -}}
{{- $root := .root }}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ .name }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "provider-kserve.labels" $root | nindent 4 }}
  annotations:
    helm.sh/hook: {{ .hook }}
    helm.sh/hook-weight: "-10"
    helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ .name }}
  labels:
    {{- include "provider-kserve.labels" $root | nindent 4 }}
  annotations:
    helm.sh/hook: {{ .hook }}
    helm.sh/hook-weight: "-10"
    helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded
rules:
  {{- toYaml .rules | nindent 2 }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ .name }}
  labels:
    {{- include "provider-kserve.labels" $root | nindent 4 }}
  annotations:
    helm.sh/hook: {{ .hook }}
    helm.sh/hook-weight: "-10"
    helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ .name }}
subjects:
  - kind: ServiceAccount
    name: {{ .name }}
    namespace: {{ $root.Release.Namespace }}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ .name }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "provider-kserve.labels" $root | nindent 4 }}
  annotations:
    helm.sh/hook: {{ .hook }}
    helm.sh/hook-weight: "-5"
    helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded
spec:
  backoffLimit: 0
  activeDeadlineSeconds: 600
  template:
    metadata:
      labels:
        helm.sh/chart: {{ include "provider-kserve.chart" $root }}
        app.kubernetes.io/managed-by: {{ $root.Release.Service }}
        app.kubernetes.io/component: {{ .hook }}
    spec:
      restartPolicy: Never
      serviceAccountName: {{ .name }}
      {{- with $root.Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 65534
        runAsGroup: 65534
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: cleanup
          image: {{ $root.Values.hookImage }}
          imagePullPolicy: IfNotPresent
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop:
                - ALL
          env:
            - name: HOME
              value: /tmp
            - name: NAMESPACE
              value: {{ $root.Release.Namespace }}
          command:
            - /bin/sh
            - -c
            - |
              {{- .script | nindent 14 }}
              exit 0
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              memory: 256Mi
          volumeMounts:
            - name: tmp
              mountPath: /tmp
      volumes:
        - name: tmp
          emptyDir: {}
      {{- with $root.Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $root.Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
{{- end }}
