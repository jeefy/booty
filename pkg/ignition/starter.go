package ignition

// StarterButane is the commented Butane template `booty init` writes to
// <dataDir>/config/ignition.yaml. It must stay a valid, near-empty config:
// hostname, SSH keys, the update timer and the booted callback come from the
// builtin fragment.
const StarterButane = `# Booty starter Butane template (rendered to Ignition for every host).
#
# Booty's builtin fragment already provides:
#   - /etc/hostname from the hardware database
#   - SSH keys for the 'core' user (--sshAuthorizedKeys / --sshAuthorizedKeysFile)
#   - the update-check timer (kured integration)
#   - the install-complete callback (POST /booted)
# so this file only needs what is specific to your fleet. Anything you define
# with the same path/unit name overrides the builtin.
#
# Go template variables available here:
#   {{ .Hostname }}     hostname from the hardware database
#   {{ .ServerIP }}     Booty's client-facing address, e.g. 192.168.1.10:8080
#   {{ .JoinString }}   --joinString (kubeadm join ...)
#   {{ .OSTreeImage }}  the host's ostreeImage (uBlue/CoreOS rebase)
#
# Butane reference: https://coreos.github.io/butane/config-fcos-v1_5/
variant: fcos
version: 1.5.0

# passwd:
#   users:
#     - name: core
#       ssh_authorized_keys:
#         - ssh-ed25519 AAAA... you@example.com

# storage:
#   files:
#     - path: /etc/systemd/resolved.conf.d/dns.conf
#       mode: 0644
#       contents:
#         inline: |
#           [Resolve]
#           DNS=192.168.1.1
#           Domains=lab.local
`
