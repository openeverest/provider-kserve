#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
CHART=${CHART_DIR:-"$ROOT/charts/provider-kserve"}
YQ_BIN=${YQ:-yq}
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

DEFAULT_MANIFEST="$TMP_DIR/default.yaml"
ENABLED_MANIFEST="$TMP_DIR/enabled.yaml"
CUSTOM_MANIFEST="$TMP_DIR/custom.yaml"

helm lint "$CHART" >/dev/null
helm template provider-kserve "$CHART" >"$DEFAULT_MANIFEST"
helm template provider-kserve "$CHART" \
  --set kserveResources.kserve.localmodel.enabled=true >"$ENABLED_MANIFEST"
helm template provider-kserve "$CHART" \
  --set kserveResources.kserve.localmodel.enabled=true \
  --set kserveResources.kserve.localmodel.jobNamespace=tenant-downloads >"$CUSTOM_MANIFEST"

resource_name() {
  kind=$1
  name=$2
  manifest=$3
  "$YQ_BIN" ea -r \
    "select(.kind == \"$kind\" and .metadata.name == \"$name\") | .metadata.name" \
    "$manifest"
}

crd_hook_command() {
  "$YQ_BIN" ea -r \
    'select(.kind == "Job") | .spec.template.spec.containers[] | select(.name == "kserve-crds") | .command[2]' \
    "$1"
}

provider_env() {
  name=$1
  manifest=$2
  "$YQ_BIN" ea -r \
    "select(.kind == \"Deployment\" and .metadata.name == \"provider-kserve\") | .spec.template.spec.containers[] | select(.name == \"provider\") | .env[] | select(.name == \"$name\") | .value" \
    "$manifest"
}

cache_ui_path() {
  "$YQ_BIN" ea -r \
    'select(.kind == "Provider") | .spec.uiSchema.llm.sections.cache.components.cacheEnabled.path' \
    "$1"
}

if [ -n "$(resource_name Deployment kserve-localmodel-controller-manager "$DEFAULT_MANIFEST")" ]; then
  echo "LocalModel controller rendered while disabled" >&2
  exit 1
fi
if [ -n "$(resource_name DaemonSet kserve-localmodelnode-agent "$DEFAULT_MANIFEST")" ]; then
  echo "LocalModel node agent rendered while disabled" >&2
  exit 1
fi
case "$(crd_hook_command "$DEFAULT_MANIFEST")" in
  *kserve-localmodel-crd*)
    echo "LocalModel CRD rendered while disabled" >&2
    exit 1
    ;;
esac
if [ "$(provider_env LOCAL_MODEL_ENABLED "$DEFAULT_MANIFEST")" != "false" ]; then
  echo "Provider LocalModel feature flag is not disabled by default" >&2
  exit 1
fi
if [ "$(cache_ui_path "$DEFAULT_MANIFEST")" != "spec.components.llmEngine.parameters.localModelCache.enabled" ]; then
  echo "Provider cache UI schema is missing" >&2
  exit 1
fi
if [ "$("$YQ_BIN" ea -r 'select(.kind == "Provider") | .spec.uiSchema.llm.sections.cache.components.cacheEnabled.uiType' "$DEFAULT_MANIFEST")" != "select" ]; then
  echo "Provider cache enable control is not renderable by the OpenEverest form" >&2
  exit 1
fi
if [ "$("$YQ_BIN" ea -r 'select(.kind == "Provider") | .spec.uiSchema.llm.sections.cache.components.cacheEnabled.fieldParams.options[1].value' "$DEFAULT_MANIFEST")" != "enabled" ]; then
  echo "Provider cache enable control does not emit the enabled mode" >&2
  exit 1
fi

if [ "$(resource_name Deployment kserve-localmodel-controller-manager "$ENABLED_MANIFEST")" != "kserve-localmodel-controller-manager" ]; then
  echo "LocalModel controller missing while enabled" >&2
  exit 1
fi
if [ "$(resource_name DaemonSet kserve-localmodelnode-agent "$ENABLED_MANIFEST")" != "kserve-localmodelnode-agent" ]; then
  echo "LocalModel node agent missing while enabled" >&2
  exit 1
fi
if [ "$("$YQ_BIN" ea -r 'select(.kind == "ConfigMap" and .metadata.name == "inferenceservice-config") | .data.localModel | from_json | .enabled' "$ENABLED_MANIFEST")" != "true" ]; then
  echo "KServe LocalModel admission config is not enabled" >&2
  exit 1
fi
if [ "$(provider_env LOCAL_MODEL_ENABLED "$ENABLED_MANIFEST")" != "true" ]; then
  echo "Provider LocalModel feature flag is not enabled" >&2
  exit 1
fi
if [ "$(provider_env LOCAL_MODEL_JOB_NAMESPACE "$ENABLED_MANIFEST")" != "kserve-localmodel-jobs" ]; then
  echo "Provider LocalModel job namespace does not match KServe" >&2
  exit 1
fi
if [ "$("$YQ_BIN" ea -r 'select(.kind == "ConfigMap" and .metadata.name == "inferenceservice-config") | .data.localModel | from_json | .jobNamespace' "$ENABLED_MANIFEST")" != "kserve-localmodel-jobs" ]; then
  echo "KServe LocalModel job namespace is not configured" >&2
  exit 1
fi
if [ "$("$YQ_BIN" ea -r 'select(.kind == "ConfigMap" and .metadata.name == "inferenceservice-config") | .data.storageInitializer | from_json | .memoryLimit' "$ENABLED_MANIFEST")" != "4Gi" ]; then
  echo "KServe LocalModel download memory limit is not configured" >&2
  exit 1
fi
if [ "$(provider_env LOCAL_MODEL_JOB_NAMESPACE "$CUSTOM_MANIFEST")" != "tenant-downloads" ]; then
  echo "Provider LocalModel custom job namespace is not configured" >&2
  exit 1
fi
if [ "$("$YQ_BIN" ea -r 'select(.kind == "ConfigMap" and .metadata.name == "inferenceservice-config") | .data.localModel | from_json | .jobNamespace' "$CUSTOM_MANIFEST")" != "tenant-downloads" ]; then
  echo "KServe LocalModel custom job namespace is not configured" >&2
  exit 1
fi
case "$(crd_hook_command "$ENABLED_MANIFEST")" in
  *kserve-localmodel-crd*) ;;
  *)
    echo "LocalModel CRD is missing while enabled" >&2
    exit 1
    ;;
esac
