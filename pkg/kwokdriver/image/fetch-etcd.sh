#!/usr/bin/env bash

set -xeuo pipefail

mkdir -p /etcd/download /etcd/bin
stat /etcd/bin

etcd="$(find "$HOME" -type f -executable | grep "etcd")"
etcd_dir="$(dirname "$etcd")"
etcd_version_output="$("$etcd" --version)"
etcd_version="$(echo "$etcd_version_output" | grep "etcd Version" | awk '{print $3;}')"
etcd_os_arch="$(echo "$etcd_version_output" | grep "OS" | awk '{print $3;}' | sed 's/\//\-/g')"
url="https://github.com/etcd-io/etcd/releases/download/v${etcd_version}/etcd-v${etcd_version}-${etcd_os_arch}.tar.gz"
echo "$url"
cd /etcd/download
curl -L "$url" | tar -xz
find /etcd/download -type f -executable -exec cp {} "$etcd_dir" \;
