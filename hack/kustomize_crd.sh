#!/bin/sh

./bin/kustomize build config/crd -o config/crd/bases

join () {
  local IFS="$1"
  shift
  echo "$*"
}

for CRD_FILENAME in $(find config/crd/bases -type f -name "apiextensions.k8s.io_v1_customresourcedefinition*"); do
    IFS='_' read -ra GVK <<< "$CRD_FILENAME"
    IFS="." read -ra GN <<< "${GVK[3]}"
    GROUP=$(join . "${GN[@]:1:$((${#GN[@]}-2))}")
    NAME="${GN[0]}"
    mv "$CRD_FILENAME" "config/crd/bases/${GROUP}_${NAME}.yaml"
done