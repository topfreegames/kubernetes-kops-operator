#!/usr/bin/env bash

if [[ ${1} == "dependencies" ]]; then
  kubectl rollout status deployment -n cert-manager cert-manager
  kubectl rollout status deployment -n cert-manager cert-manager-cainjector
  kubectl rollout status deployment -n cert-manager cert-manager-webhook

elif [[ ${1} == "cluster-api" ]]; then
  kubectl rollout status deployment -n capi-system capi-controller-manager
fi

sleep 60