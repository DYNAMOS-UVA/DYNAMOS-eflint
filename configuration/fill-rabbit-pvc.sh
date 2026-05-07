#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Create the temporary pod
kubectl apply -f "${SCRIPT_DIR}/temp-pod.yaml"

# Wait for the pod to be in the 'Running' state
echo "Waiting for temp-pod to be Running..."
kubectl wait --for=condition=Ready pod/temp-pod --timeout=300s -n core
kubectl wait --for=condition=Ready pod/temp-pod-orch --timeout=300s -n orchestrator

# Copy local files to the PVC
kubectl cp "${SCRIPT_DIR}/k8s_service_files/definitions.json" temp-pod:/mnt/ -n core
kubectl cp "${SCRIPT_DIR}/k8s_service_files/rabbitmq.conf" temp-pod:/mnt/ -n core

# Tar etcd JSON config and eFLINT models (orchestrator reads /app/etcd/eflint-models on the same PVC)
STAGING="$(mktemp -d)"
trap 'rm -rf "${STAGING}"' EXIT
cp -a "${SCRIPT_DIR}/etcd_launch_files/." "${STAGING}/"
cp -a "${SCRIPT_DIR}/eflint-models" "${STAGING}/"
tar -czvf "${SCRIPT_DIR}/etcd_files.tar.gz" -C "${STAGING}" .

# Copy the tarball to the pod
kubectl cp "${SCRIPT_DIR}/etcd_files.tar.gz" temp-pod-orch:/mnt -n orchestrator

# Untar the files inside the pod (optional, if you want to unpack the files inside the pod)
kubectl exec -n orchestrator temp-pod-orch -- tar -xzvf /mnt/etcd_files.tar.gz -C /mnt

# Delete the temporary pod
kubectl delete -f "${SCRIPT_DIR}/temp-pod.yaml"
