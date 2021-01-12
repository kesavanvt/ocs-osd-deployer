#! /bin/bash

# Copyright 2020 Red Hat, Inc. and/or its affiliates.
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

set -e -o pipefail

#-- The Quota (SKU count) comes in as an env var, and we multiply by 3 to get
#-- the count of OSDs we need to start due to 3x replication
osds=$((${QUOTA_COUNT:-1} * 3))

echo "Quota count: ${QUOTA_COUNT} -- OSD count: ${osds}"

#-- Get all worker nodes
nodeList=( $(kubectl get nodes -l node-role.kubernetes.io/worker=,node-role.kubernetes.io/infra!= \
--no-headers -o custom-columns=":metadata.name") )

#-- Label all worker nodes
for node in "${nodeList[@]}"
do
kubectl label nodes $node cluster.ocs.openshift.io/openshift-storage=''
done

#-- The path mapped to StorageCluster configmap used for overriding.
overridePath="/sc-override/storagecluster.yml"

#-- Patch the OSD count into the StorageCluster & apply it
#-- If config map containing StorageCluster exists, apply it. 
#-- Else, apply the default StorageCluster.
while true; do
    storageClusterPath="storagecluster.yml"
    [ -f $overridePath ] && storageClusterPath=$overridePath
    sed "s/STORAGE_NODES/${osds}/" $storageClusterPath | \
      kubectl -n openshift-storage apply -f -
    sleep 60
done

echo "Exiting..."
