#!/bin/bash
# KWOK-based MCO/MCC Integration Test
#
# Brings up a KWOK simulated OpenShift cluster, applies the necessary CRDs and
# config objects, then starts the Machine Config Operator and Machine Config
# Controller in separate containers connected to it.
#
# Usage: ./kwok-mco-integration.sh [--no-cleanup] [--skip-verify]

set -euo pipefail

###############################################################################
# Configuration
###############################################################################
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMPDIR=$(mktemp -d /tmp/kwok-mco-XXXXX)
CA_DIR="$TMPDIR/certs"
IMAGES_JSON="/images.json"

mkdir -p "$CA_DIR"

###############################################################################
# Functions
###############################################################################
log() { echo "==> $(date +%H:%M:%S) $*"; }

wait_for() {
  local desc="$1"
  local timeout="$2"
  shift 2
  log "Waiting for $desc (timeout: ${timeout}s)..."
  local end=$((SECONDS + timeout))
  until "$@" >/dev/null 2>&1; do
    if [ $SECONDS -ge $end ]; then
      log "TIMEOUT waiting for $desc"
      return 1
    fi
    sleep 2
  done
  log "$desc: ready"
}

get_image() {
  echo "$RELEASE_INFO" | jq -r --arg name "$1" \
    '.references.spec.tags[] | select(.name==$name) | .from.name'
}

kubectl_apply_yaml() {
  kubectl apply -f - <<ENDOFYAML
$1
ENDOFYAML
}

retry() {
  local attempts="$1"
  shift
  local delay="$1"
  shift
  local i
  for ((i = 1; i <= attempts; i++)); do
    if "$@" 2>&1; then
      return 0
    fi
    [ "$i" -lt "$attempts" ] && sleep "$delay"
  done
  return 1
}

log "Phase 2: Starting KWOK cluster"
kwokctl create cluster

###############################################################################
# Phase 4: Apply CRDs via utility container
###############################################################################
log "Phase 4: Applying CRDs from release image"

# Pull the release image so it can be mounted
crds=$(find /release-image/release-manifests /release-image/manifests -name "*.crd.yaml" 2>/dev/null | sort)
count=$(echo "$crds" | wc -l)
echo "Found $count CRD files"
for f in $crds; do
  echo "  Applying: $(basename $f)"
  kubectl apply -f "$f" 2>&1 || true &
done
wait
echo "CRD application complete"

log "CRDs applied"

log "Waiting for CRDs to become established..."
sleep 5

###############################################################################
# Phase 6: Create namespaces
###############################################################################
log "Phase 6: Creating namespaces"
NAMESPACES=(
  openshift-machine-config-operator
  openshift-config
  openshift-config-managed
  openshift-kube-apiserver-operator
  openshift-cluster-version
  openshift-machine-api
  openshift-openstack-infra
  openshift-kni-infra
  openshift-ovirt-infra
  openshift-vsphere-infra
  openshift-nutanix-infra
  openshift-cloud-platform-infra
)
for ns in "${NAMESPACES[@]}"; do
  kubectl create namespace "$ns" 2>/dev/null || true &
done
wait
log "Namespaces created"

###############################################################################
# Phase 7: Create RBAC
###############################################################################
log "Phase 7: Creating RBAC"
kubectl_apply_yaml '
apiVersion: v1
kind: ServiceAccount
metadata:
  name: machine-config-operator
  namespace: openshift-machine-config-operator
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: custom-account-openshift-machine-config-operator
subjects:
- kind: ServiceAccount
  name: machine-config-operator
  namespace: openshift-machine-config-operator
roleRef:
  kind: ClusterRole
  name: cluster-admin
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: machine-config-controller
  namespace: openshift-machine-config-operator
'
log "RBAC created"

###############################################################################
# Phase 8: Create cluster config objects
###############################################################################
log "Phase 8: Creating cluster config objects"

kubectl_apply_yaml '
apiVersion: config.openshift.io/v1
kind: Infrastructure
metadata:
  name: cluster
spec:
  cloudConfig:
    name: ""
  platformSpec:
    type: None
'

retry 5 2 kubectl patch infrastructure cluster --subresource=status --type=merge -p '{
  "status": {
    "apiServerInternalURI": "https://api-int.kwok-test.example.com:6443",
    "apiServerURL": "https://api.kwok-test.example.com:6443",
    "controlPlaneTopology": "HighlyAvailable",
    "infrastructureTopology": "HighlyAvailable",
    "infrastructureName": "kwok-test",
    "platform": "None",
    "platformStatus": {"type": "None"}
  }
}'

kubectl_apply_yaml '
apiVersion: config.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  clusterNetwork:
  - cidr: 10.128.0.0/14
    hostPrefix: 23
  networkType: OVNKubernetes
  serviceNetwork:
  - 172.30.0.0/16
'

retry 5 2 kubectl patch network.config.openshift.io cluster --type=merge -p '{
  "status": {
    "networkType": "OVNKubernetes",
    "clusterNetwork": [{"cidr": "10.128.0.0/14", "hostPrefix": 23}],
    "serviceNetwork": ["172.30.0.0/16"]
  }
}'

kubectl_apply_yaml '
apiVersion: config.openshift.io/v1
kind: DNS
metadata:
  name: cluster
spec:
  baseDomain: kwok-test.example.com
'

kubectl_apply_yaml '
apiVersion: config.openshift.io/v1
kind: Proxy
metadata:
  name: cluster
spec: {}
'

kubectl_apply_yaml '
apiVersion: config.openshift.io/v1
kind: FeatureGate
metadata:
  name: cluster
spec:
  featureSet: DevPreviewNoUpgrade
'

CLUSTER_ID=$(uuidgen)
kubectl_apply_yaml "
apiVersion: config.openshift.io/v1
kind: ClusterVersion
metadata:
  name: version
spec:
  clusterID: $CLUSTER_ID
"

kubectl_apply_yaml '
apiVersion: config.openshift.io/v1
kind: Scheduler
metadata:
  name: cluster
spec:
  mastersSchedulable: false
'

kubectl_apply_yaml '
apiVersion: config.openshift.io/v1
kind: Ingress
metadata:
  name: cluster
spec:
  domain: apps.kwok-test.example.com
'

kubectl_apply_yaml '
apiVersion: config.openshift.io/v1
kind: APIServer
metadata:
  name: cluster
spec: {}
'

kubectl_apply_yaml '
apiVersion: config.openshift.io/v1
kind: Image
metadata:
  name: cluster
spec: {}
'

kubectl_apply_yaml '
apiVersion: operator.openshift.io/v1
kind: MachineConfiguration
metadata:
  name: cluster
spec: {}
'

log "Cluster config objects created"

###############################################################################
# Phase 9: Generate and apply certificates
###############################################################################
log "Phase 9: Generating certificates"

openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout "$CA_DIR/ca.key" \
  -out "$CA_DIR/ca.crt" \
  -subj "/CN=kwok-test-ca" 2>/dev/null

kubectl create configmap machine-config-server-ca \
  --from-file=ca-bundle.crt="$CA_DIR/ca.crt" \
  -n openshift-machine-config-operator 2>/dev/null || true

kubectl create configmap root-ca \
  --from-file=ca.crt="$CA_DIR/ca.crt" \
  -n kube-system 2>/dev/null || true

kubectl create configmap initial-kube-apiserver-server-ca \
  --from-file=ca-bundle.crt="$CA_DIR/ca.crt" \
  -n openshift-config 2>/dev/null || true

kubectl create configmap kube-apiserver-to-kubelet-client-ca \
  --from-file=ca-bundle.crt="$CA_DIR/ca.crt" \
  -n openshift-kube-apiserver-operator 2>/dev/null || true

log "Certificates created"

###############################################################################
# Phase 10: Patch status subresources (ClusterVersion, FeatureGate)
###############################################################################
log "Phase 10: Patching status subresources"

# Patch ClusterVersion status
CV_STATUS=$(python3 -c "
import yaml, json, sys

ref_path = '$SCRIPT_DIR/clusterversion.yaml'
try:
    with open(ref_path) as f:
        data = yaml.safe_load(f)
    status = data.get('status', {})
except FileNotFoundError:
    status = {
        'conditions': [
            {'type': 'Available', 'status': 'True', 'message': 'Done applying', 'reason': 'ClusterStatusReady',
             'lastTransitionTime': '2026-01-01T00:00:00Z'},
            {'type': 'Progressing', 'status': 'False', 'message': 'Cluster version is stable',
             'lastTransitionTime': '2026-01-01T00:00:00Z'},
            {'type': 'Failing', 'status': 'False',
             'lastTransitionTime': '2026-01-01T00:00:00Z'},
            {'type': 'ReleaseAccepted', 'status': 'True', 'reason': 'PayloadLoaded',
             'message': 'Payload loaded', 'lastTransitionTime': '2026-01-01T00:00:00Z'},
        ],
        'availableUpdates': None,
    }

release_version = '$RELEASE_VERSION'
release_image = '$OPENSHIFT_RELEASE_IMAGE'
if status.get('availableUpdates') is None:
    status['availableUpdates'] = []
status['desired'] = {'version': release_version, 'image': release_image}
for h in status.get('history', []):
    h['version'] = release_version
    h['image'] = release_image
if not status.get('history'):
    status['history'] = [{
        'version': release_version,
        'image': release_image,
        'state': 'Completed',
        'verified': False,
        'startedTime': '2026-01-01T00:00:00Z',
        'completionTime': '2026-01-01T00:00:00Z',
    }]
for c in status.get('conditions', []):
    if 'PayloadLoaded' in c.get('reason', '') or 'Payload' in c.get('message', ''):
        c['message'] = f'Payload loaded version=\"{release_version}\" image=\"{release_image}\"'
    if c.get('type') == 'Available':
        c['message'] = f'Done applying {release_version}'
    if c.get('type') == 'Progressing':
        c['message'] = f'Cluster version is {release_version}'

json.dump({'status': status}, sys.stdout)
")

retry 5 2 kubectl patch clusterversion version --subresource=status --type=merge -p "$CV_STATUS"
log "ClusterVersion status patched"

# Patch FeatureGate status
FG_STATUS=$(python3 -c "
import yaml, json, sys

ref_path = '$SCRIPT_DIR/featuregates.yaml'
try:
    with open(ref_path) as f:
        data = yaml.safe_load(f)
    status = data.get('status', {})
except FileNotFoundError:
    status = {'featureGates': [{'version': '', 'enabled': [{'name': 'OSStreams'}], 'disabled': []}]}

release_version = '$RELEASE_VERSION'
for fg in status.get('featureGates', []):
    fg['version'] = release_version

json.dump({'status': status}, sys.stdout)
")

retry 5 2 kubectl patch featuregate cluster --subresource=status --type=merge -p "$FG_STATUS"
log "FeatureGate status patched"

###############################################################################
# Phase 11: Create ConfigMaps and Secrets
###############################################################################
log "Phase 11: Creating ConfigMaps and Secrets"

# Pull secret (minimal empty auth)
kubectl_apply_yaml '
apiVersion: v1
kind: Secret
metadata:
  name: pull-secret
  namespace: openshift-config
type: kubernetes.io/dockerconfigjson
stringData:
  .dockerconfigjson: "{\"auths\":{}}"
'

# images.json ConfigMap
kubectl create configmap machine-config-operator-images \
  --from-file=images.json="$IMAGES_JSON" \
  -n openshift-machine-config-operator 2>/dev/null || true

# cluster-config-v1 (minimal install-config)
kubectl_apply_yaml "
apiVersion: v1
kind: ConfigMap
metadata:
  name: cluster-config-v1
  namespace: kube-system
data:
  install-config: |
    apiVersion: v1
    metadata:
      name: kwok-test
    baseDomain: kwok-test.example.com
    platform:
      none: {}
    networking:
      networkType: OVNKubernetes
      clusterNetwork:
      - cidr: 10.128.0.0/14
        hostPrefix: 23
      serviceNetwork:
      - 172.30.0.0/16
"

# machine-config-osimageurl ConfigMap (required by OSImageStream controller)
kubectl create configmap machine-config-osimageurl \
  --from-literal=baseOSContainerImage="$RHEL_COREOS_IMAGE" \
  --from-literal=baseOSExtensionsContainerImage="$RHEL_COREOS_EXT_IMAGE" \
  --from-literal=osImageURL="$RHEL_COREOS_IMAGE" \
  --from-literal=releaseVersion="$RELEASE_VERSION" \
  -n openshift-machine-config-operator 2>/dev/null || true

log "ConfigMaps and Secrets created"

###############################################################################
# Phase 11b: Create OSImageStream
###############################################################################
log "Phase 11b: Creating OSImageStream"

kubectl_apply_yaml "
apiVersion: machineconfiguration.openshift.io/v1
kind: OSImageStream
metadata:
  name: cluster
  annotations:
    machineconfiguration.openshift.io/release-payload-image: \"$OPENSHIFT_RELEASE_IMAGE\"
    machineconfiguration.openshift.io/release-image-version: \"$MCO_VERSION_HASH\"
spec:
  defaultStream: \"5.0\"
"

retry 5 2 kubectl patch osimagestream cluster --subresource=status --type=merge -p "{
  \"status\": {
    \"defaultStream\": \"5.0\",
    \"availableStreams\": [
      {
        \"name\": \"5.0\",
        \"osImage\": \"${RHEL_COREOS_IMAGE}\",
        \"osExtensionsImage\": \"${RHEL_COREOS_EXT_IMAGE}\"
      }
    ]
  }
}"

log "OSImageStream created"

###############################################################################
# Phase 12: Create simulated nodes
###############################################################################
log "Phase 12: Creating simulated nodes"

for i in 0 1 2; do
  kubectl apply -f - <<EOF
apiVersion: v1
kind: Node
metadata:
  annotations:
    kwok.x-k8s.io/node: "fake"
  labels:
    kubernetes.io/hostname: "kwok-master-${i}"
    kubernetes.io/os: linux
    kubernetes.io/arch: amd64
    type: kwok
    node-role.kubernetes.io/master: ""
    node-role.kubernetes.io/control-plane: ""
  name: "kwok-master-${i}"
spec: {}
status:
  allocatable:
    cpu: "16"
    memory: 64Gi
    pods: "110"
  capacity:
    cpu: "16"
    memory: 64Gi
    pods: "110"
  nodeInfo:
    architecture: amd64
    operatingSystem: linux
    kubeletVersion: v1.36.1
    osImage: "Red Hat Enterprise Linux CoreOS"
  phase: Running
EOF
done

for i in 0 1 2; do
  kubectl apply -f - <<EOF
apiVersion: v1
kind: Node
metadata:
  annotations:
    kwok.x-k8s.io/node: "fake"
  labels:
    kubernetes.io/hostname: "kwok-worker-${i}"
    kubernetes.io/os: linux
    kubernetes.io/arch: amd64
    type: kwok
    node-role.kubernetes.io/worker: ""
  name: "kwok-worker-${i}"
spec: {}
status:
  allocatable:
    cpu: "16"
    memory: 64Gi
    pods: "110"
  capacity:
    cpu: "16"
    memory: 64Gi
    pods: "110"
  nodeInfo:
    architecture: amd64
    operatingSystem: linux
    kubeletVersion: v1.36.1
    osImage: "Red Hat Enterprise Linux CoreOS"
  phase: Running
EOF
done

log "Nodes created"
kubectl get nodes -o wide 2>/dev/null || true

kwokctl snapshot save --path /root/cluster.db
kwokctl stop cluster
