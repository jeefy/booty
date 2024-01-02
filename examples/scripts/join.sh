#!/bin/bash
modprobe br_netfilter
sysctl net.bridge.bridge-nf-call-iptables=1
sysctl net.ipv4.ip_forward=1
export PATH=/opt/bin/:$PATH
kubeadm reset -f
${JOIN_STRING}