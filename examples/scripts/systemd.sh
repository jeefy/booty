#!/bin/bash
curl -sSL "https://raw.githubusercontent.com/kubernetes/release/master/cmd/krel/templates/latest/kubelet/kubelet.service" | sed "s:/usr/bin:/opt/bin/:g" > /etc/systemd/system/kubelet.service
mkdir -p /etc/systemd/system/kubelet.service.d
curl -sSL "https://raw.githubusercontent.com/kubernetes/release/master/cmd/krel/templates/latest/kubeadm/10-kubeadm.conf" | sed "s:/usr/bin:/opt/bin/:g" > /etc/systemd/system/kubelet.service.d/10-kubeadm.conf

echo "KUBELET_EXTRA_ARGS=--cgroup-driver=systemd --fail-swap-on=false" > /etc/default/kubelet
echo "KUBELET_EXTRA_ARGS=--cgroup-driver=systemd --fail-swap-on=false" > /etc/sysconfig/kubelet

systemctl enable kubelet && systemctl start kubelet
echo "Kubelet started";