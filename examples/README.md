# Booty Example Config

This folder contains example Butane templates Booty can use to iPXE boot machines, plus the helper scripts the clients fetch from `/data/config/` during startup.

* [`config/ignition.yaml`](config/ignition.yaml) -- Flatcar node that mounts an ephemeral disk for containerd and joins a kubeadm cluster.
* [`ucore.but`](ucore.but) -- uCore (Fedora CoreOS based) node that rebases onto the image cached by Booty and joins a kubeadm cluster.
* [`bazzite.but`](bazzite.but) -- Bazzite desktop that rebases onto a `bazzite-nvidia` image.
* [`k8s.yaml`](k8s.yaml) -- Kubernetes Deployment/ConfigMap/Service for Booty itself.
* [`scripts/`](scripts/) -- `cni.sh`, `kube-tools.sh`, `systemd.sh`, `join.sh`, referenced by the templates above.

The templates only contain what is specific to that fleet. Booty merges its **builtin fragment** into every registered host's Ignition (see the *Composition* section of the main README): `/etc/hostname` from the hardware database, the `core` user's SSH keys from `--sshAuthorizedKeysFile`/`--sshAuthorizedKeys`, the `booty-booted.service` install-complete callback, and the `booty-update.timer` that asks `GET /update-check` every 10 minutes and touches `/var/run/reboot-required` for [kured](https://github.com/kubereboot/kured) when the host is behind what Booty serves. The old `version-check.sh` script is gone; the check lives in Booty now.

Anything you put in your own template with the same file path or unit name wins over the builtin, so you can still override e.g. the timer schedule.
