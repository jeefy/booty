#!/bin/bash
sysctl net.bridge.bridge-nf-call-iptables=1
/opt/bin/${JOIN_STRING}