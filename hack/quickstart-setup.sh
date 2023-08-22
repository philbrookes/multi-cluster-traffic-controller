#!/bin/bash

#
# Copyright 2022 Red Hat, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

source /dev/stdin <<< "$(curl -s https://raw.githubusercontent.com/kuadrant/multicluster-gateway-controller/main/hack/.quickstartEnv)"
source /dev/stdin <<< "$(curl -s https://raw.githubusercontent.com/kuadrant/multicluster-gateway-controller/main/hack/.kindUtils)"
source /dev/stdin <<< "$(curl -s https://raw.githubusercontent.com/kuadrant/multicluster-gateway-controller/main/hack/.cleanupUtils)"
source /dev/stdin <<< "$(curl -s https://raw.githubusercontent.com/kuadrant/multicluster-gateway-controller/main/hack/.deployUtils)"
source /dev/stdin <<< "$(curl -s https://raw.githubusercontent.com/kuadrant/multicluster-gateway-controller/main/hack/.startUtils)"
source /dev/stdin <<< "$(curl -s https://raw.githubusercontent.com/kuadrant/multicluster-gateway-controller/main/hack/.setupEnv)"

export OPERATOR_SDK_BIN=$(dockerBinCmd "operator-sdk")
export YQ_BIN=$(dockerBinCmd "yq")
export CLUSTERADM_BIN=$(dockerBinCmd "clusteradm")

MGC_REPO="github.com/kuadrant/multicluster-gateway-controller.git"
QUICK_START_HUB_KUSTOMIZATION=${MGC_REPO}/config/quick-start/control-cluster
QUICK_START_SPOKE_KUSTOMIZATION=${MGC_REPO}/config/quick-start/workload-cluster

set -e pipefail

# Prompt user for any required env vars that have not been set
requiredENV

# Default config
if [[ -z "${LOG_LEVEL}" ]]; then
  LOG_LEVEL=1
fi
if [[ -z "${OCM_SINGLE}" ]]; then
  OCM_SINGLE=true
fi
if [[ -z "${MGC_WORKLOAD_CLUSTERS_COUNT}" ]]; then
  MGC_WORKLOAD_CLUSTERS_COUNT=1
fi

# Make temporary directory for kubeconfig
mkdir -p ${TMP_DIR}

cleanupKind

kindSetupMGCClusters ${KIND_CLUSTER_CONTROL_PLANE} ${KIND_CLUSTER_WORKLOAD} ${port80} ${port443} ${MGC_WORKLOAD_CLUSTERS_COUNT}

# Apply Cluster Configurations to Control cluster
# Deploy OCM hub
deployOCMHub ${KIND_CLUSTER_CONTROL_PLANE} "minimal"
# Deploy Quick start kustomize
deployQuickStartControl ${KIND_CLUSTER_CONTROL_PLANE}
# Initialize local dev setup for the controller on the control-plane cluster
configureController ${KIND_CLUSTER_CONTROL_PLANE}
# Deploy MetalLb
configureMetalLB ${KIND_CLUSTER_CONTROL_PLANE} ${metalLBSubnetStart}
configureControlCluster ${KIND_CLUSTER_CONTROL_PLANE}


# Apply Cluster Configurations to Workload clusters
if [[ -n "${MGC_WORKLOAD_CLUSTERS_COUNT}" ]]; then
  for ((i = 1; i <= ${MGC_WORKLOAD_CLUSTERS_COUNT}; i++)); do
    deployQuickStartWorkload ${KIND_CLUSTER_WORKLOAD}-${i}
    configureMetalLB ${KIND_CLUSTER_WORKLOAD}-${i} $((${metalLBSubnetStart} + ${i}))
    deployOLM ${KIND_CLUSTER_WORKLOAD}-${i}
    deployOCMSpoke ${KIND_CLUSTER_WORKLOAD}-${i}
  done
fi


kubectl config use-context kind-${KIND_CLUSTER_CONTROL_PLANE}


echo ""
echo "What's next...

      Now that you have 2 kind clusters configured and with multicluster-gateway-controller installed you are ready to begin creating gateways
      Visit https://docs.kuadrant.io/multicluster-gateway-controller/docs/how-to/ocm-control-plane-walkthrough/#create-a-gateway for next steps"