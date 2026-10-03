# Booty Example Config

This folder contains example Butane templates Booty can use to iPXE boot machines and a Kubernetes manifest for running Booty itself.

* [`config/ignition.yaml`](config/ignition.yaml) -- site-only template for Flatcar workers (DNS, motd). Everything Kubernetes-related comes from `--profile=kubeadm-worker`.
* [`ucore.but`](ucore.but) -- uCore (Fedora CoreOS based, registered as `coreos` with an `ostreeImage`) node that rebases onto its image, installs Kubernetes with rpm-ostree and joins a kubeadm cluster with its own inline join script.
* [`bazzite.but`](bazzite.but) -- Bazzite desktop (registered as `coreos` with an `ostreeImage`) that rebases onto a `bazzite-nvidia` image.

Bluefin Server hosts need no template here either: Booty renders each one a per-host Ignition config (`bluefin-node.ign`, served next to its netboot UKI) from its host fields -- hostname, SSH keys, `stateDisk`, `extensions`, k0s or kubeadm join -- and a per-host `ignitionFile` template is merged last when set (the global `--ignitionFile` is not). See the *Bluefin Server* section of the main README.
* [`k8s.yaml`](k8s.yaml) -- Booty in `kube-system` on the control-plane node: ServiceAccount + RBAC for automatic join tokens, ConfigMap with the site template, Secret with SSH keys, Deployment (`hostNetwork`, `/healthz` probes, minimal capabilities) and the two MetalLB Services.

The templates only contain what is specific to that fleet. Booty merges its **builtin fragment** into every registered host's Ignition (see the *Composition* section of the main README): `/etc/hostname` from the hardware database, the `core` user's SSH keys from `--sshAuthorizedKeysFile`/`--sshAuthorizedKeys`, the `booty-booted.service` install-complete callback, and the `booty-update.timer` that asks `GET /update-check` every 10 minutes and touches `/var/run/reboot-required` for [kured](https://github.com/kubereboot/kured) when the host is behind what Booty serves.

With `--profile=kubeadm-worker` Booty also appends the **kubeadm worker profile** to every `flatcar`/`coreos` host: the CNI plugins, kubeadm/kubelet/kubectl/crictl (static binaries on Flatcar and Fedora CoreOS alike), the kubelet systemd units, an optional wiped-on-boot containerd disk (`--containerdDisk`), and a `booty-k8s-join.service` that runs `kubeadm reset; kubeadm join ...` on every boot. The scripts are embedded in the Ignition config (nothing is fetched from `/data/config/` any more); the join string is `--joinString`/`--joinStringFile`, or a fresh one-hour bootstrap token per boot with `--kubeadmJoin=auto`. See the *kubeadm worker profile* and *Automatic join tokens* sections of the main README.

Anything you put in your own template with the same file path or unit name wins over the builtin and the profile, so you can still override e.g. the timer schedule or a profile unit.
