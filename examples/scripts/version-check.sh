#!/bin/bash

set -a
. /etc/lsb-release
. <(curl http://your-server-ip/version.txt)
set +a

echo "Local version: $DISTRIB_RELEASE";
echo "Remote version: $FLATCAR_VERSION";

if [ "$DISTRIB_RELEASE" != "$FLATCAR_VERSION" ]; then
	echo "Need to reboot!";
	touch /var/run/reboot-required
else
	echo "Up to date";
fi


