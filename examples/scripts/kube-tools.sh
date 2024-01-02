#!/bin/bash

set -a
. /etc/os-release
if [ $VARIANT_ID == "fedora" ]; then
    echo "Fedora detected.";
    echo "TODO: Install kube tools for Fedora";
fi


RELEASE="$(curl -sSL https://dl.k8s.io/release/stable.txt)"
mkdir -p /opt/bin//
cd /opt/bin// || exit
curl -L --remote-name-all https://storage.googleapis.com/kubernetes-release/release/${RELEASE}/bin/linux/amd64/{kubeadm,kubelet,kubectl}
chmod +x {kubeadm,kubelet,kubectl}
wget https://github.com/kubernetes-incubator/cri-tools/releases/download/$RELEASE/crictl-$RELEASE-linux-amd64.tar.gz
sudo tar zxvf crictl-$RELEASE-linux-amd64.tar.gz -C /opt/bin/
rm -f crictl-$RELEASE-linux-amd64.tar.gz
mkdir -p /etc/kubernetes/manifests
echo "Kube Tools installed.";