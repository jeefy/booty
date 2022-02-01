#!/bin/bash
MAC=$(ifconfig eno1 | awk '/ether/ {print $2}')
HOSTNAME=$(curl --fail http://${BOOTY_IP}/hosts"?mac=$MAC")

RET=$?
if [ $RET -ne 0 ]; then
        echo "Failed to get hostname from server"
        HOSTNAME=flatcar
fi

echo "$HOSTNAME" > /etc/hostname
sudo hostnamectl set-hostname "$HOSTNAME"
