# Booty Example Config

This folder contains an example Ignition/Container Linux config file that Booty can use to PXE boot machines. Included are the scripts that the clients will use during their startup.

The machines will boot into the latest `stable` channel Flatcar-Linux OS and, once started, update their hostnames based on their MAC address to the hostname stored in Booty's hardware JSON file.