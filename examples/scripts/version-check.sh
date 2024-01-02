#!/bin/bash

set -a
. /etc/os-release
. <(curl http://${BOOTY_IP}/version.txt)
set +a

VERSION_COMPARISON="LOCAL";
REMOTE_COMPARISON="REMOTE";

if [[ $ID == "coreos" ]]; then
	# First set default values for vanilla Silverblue/CoreOS
	VERSION_COMPARISON=$OSTREE_VERSION;
	REMOTE_COMPARISON=$COREOS_VERSION;

	# Check to see if this is an OSTree image
	ociImageName=$(rpm-ostree status -b --json | jq '.deployments[0]."container-image-reference"')
	ociDigest=$(rpm-ostree status -b --json | jq '.deployments[0]."container-image-reference-digest"')
	if [[ $ociImageName == "ostree-unverified-registry"* ]]; then
		# This is a shitty but simple hack to check if they match :) 
		# Does the local image SHA exist in the remote registry?
		# If so, just pass
		if [[ $(curl http://${BOOTY_IP}/registry) == *$ociDigest* ]]; then
			# This is an OSTree image
			VERSION_COMPARISON="PASS";
			REMOTE_COMPARISON="PASS";
		fi
	fi
fi
if [[ $ID == "flatcar" ]]; then
	VERSION_COMPARISON=$VERSION;
	REMOTE_COMPARISON=$FLATCAR_VERSION;
fi


echo "Local version: $VERSION_COMPARISON";
echo "Remote version: $VERSION_COMPARISON";

if [ "$REMOTE_COMPARISON" != "$VERSION_COMPARISON" ]; then
	echo "Need to reboot!";
	touch /var/run/reboot-required
else
	echo "Up to date";
fi