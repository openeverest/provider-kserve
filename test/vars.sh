#!/bin/bash

## ===== Environment variables for the provider-kserve integration tests =====
export PROVIDER_ROOT_PATH=${PROVIDER_ROOT_PATH:-${PWD}}
echo "PROVIDER_ROOT_PATH=${PROVIDER_ROOT_PATH}"

## Default version bundle exercised by the tests. Keep in sync with the
## provider's default version bundle (definition/versions.yaml).
export KSERVE_VERSION_BUNDLE=${KSERVE_VERSION_BUNDLE:-"0.15"}
echo "KSERVE_VERSION_BUNDLE=${KSERVE_VERSION_BUNDLE}"
