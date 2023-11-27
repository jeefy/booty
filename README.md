# Booty

A simple (i)PXE Server for booting Flatcar-Linux and CoreOS

```
> booty --help

Easy iPXE server for Flatcar

Usage:
  booty [flags]

Flags:
      --coreOSArchitecture string    Architecture to use for CoreOS downloads (default "x86_64")
      --coreOSChannel string         CoreOS channel to look for updates (default "stable")
      --dataDir string               Directory to store stateful data (default "/data")
      --debug                        Enable debug logging
      --flatcarArchitecture string   Architecture to use for the Flatcar downloads (default "amd64")
      --flatcarChannel string        Flatcar channel to look for updates (default "stable")
  -h, --help                         help for booty
      --httpPort int                 Port to use for the HTTP server (default 8080)
      --joinString string            The kubeadm join string to use to auto-join to a K8s cluster (kubeadm join 192.168.1.10:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:SHA_HASH
      --serverHttpPort int           Alternative HTTP port to use for clients (default 80)
      --serverIP string              IP address that clients can connect to (default "127.0.0.1")
      --updateSchedule string        Cron schedule to use for cleaning up cache files (default "*/5 * * * *")
```

## Features

* (i)PXE boot into the latest Flatcar-Linux or CoreOS
* MAC address based hostnames
* Automatic conversion of Butane YAML to Ignition JSON
  * Variable injection in Butane/Ignition
* JSON "Hardware Database" (Containing boot-time config data)
* Automatic updates retrieved from Flatcar-Linux and CoreOS
* Automatic drain/reboot of nodes (in conjunction with [Kured](https://github.com/weaveworks/kured))
* Web UI to add/edit/remove hosts
* Unrecognized MAC addresses go into the brig (boot loop till the MAC is registered)
* Support for different operating systems and ignition files per machine
* **EXPERIMENTAL**: Support for per-ostree images per machine (in conjunction with ignition rebase scripts)


## Examples

[Example ignition config / helper scripts](examples/README.md)

### Docker

```
docker run --rm -it \
--network=host \
-v $PWD:/data/ \
ghcr.io/jeefy/booty:main \
--dataDir=/storage/ \
--joinString="kubeadm join 192.168.1.10:6443 --token ${TOKEN} --discovery-token-ca-cert-hash sha256:${SHA_HASH}
--serverIP=192.168.1.10
--serverHttpPort=8080
```

### Kubernetes

[Example deployment](examples/k8s.yaml)

This creates a configmap with the example ignition yaml config, scripts, a deployment of booty, and a service.

### PXE vs iPXE

The boot target file is different depending on whether you want to use PXE or iPXE. While iPXE is recommended due to performance, there may be some use cases where PXE is required.

To boot into PXE, use `pxelinux.cfg/default`

To boot into iPXE, use `undionly.kpxe`

## Additional Thoughts

**Why?**

I like treating (most of) my machines like cattle. This is an easier and more lightweight way to tackle PXE booting and patch management.

**Can you make it do X?**

Feature requests / optimizations / PRs are welcome! Feel free to ping me [@jeefy](https://twitter.com/jeefy) on Twitter.