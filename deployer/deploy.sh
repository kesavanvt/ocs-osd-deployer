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

#-- Patch the OSD count into the StorageCluster & apply it
#-- If config map containing StorageCluster exists, merge it with the 
#-- existing StorageCluster
while true; do
    sc_content=`kubectl get configmaps storagecluster -n openshift-storage -oyaml 2> /dev/null`
    if [ $? -ne 0 ]
    then
        sed "s/STORAGE_NODES/${osds}/" storagecluster.yml | \
        kubectl -n openshift-storage apply -f -
    else
        echo "configmap is used to merge StorageCluster properties"
        echo "$sc_content" | yq r - "data.*" | yq merge - storagecluster.yml | \
        kubectl -n openshift-storage apply -f -
    fi
    sleep 60
done

echo "Exiting..."
