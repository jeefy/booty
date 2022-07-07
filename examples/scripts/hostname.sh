#!/bin/bash
MAC=$(ifconfig $(ip addr | awk '/state UP/ {print $2}' | sed 's/.$//') | awk '/ether/ {print $2}')
HOSTNAME=$(curl --fail http://${BOOTY_IP}/hosts?mac="$MAC" | jq -r '.hostname')

RET=$?
if [ $RET -ne 0 ]; then
        echo "Failed to get hostname from server"
        HOSTNAME=flatcar
fi

echo "$HOSTNAME" > /etc/hostname
sudo hostnamectl set-hostname "$HOSTNAME"