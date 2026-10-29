# Configuration: Flags, Environment and the Config Editor

The full flag reference, the `BOOTY_<FLAG>` environment convention, and the in-UI configuration view and Butane template editor.

```
> booty --help
Easy iPXE server for Flatcar, CoreOS, Bluefin Server, and more

Usage:
  booty [flags]
  booty [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  init        Create a data directory with a starter Butane template and an empty hardware map
  node-reboot Reboot (or, with --poweroff, power off) the host this runs on: sync, then SIGRTMIN+5 (or +4) to PID 1

Flags:
      --allowUnverifiedFedoraRPMs          Install unpinned Fedora RPMs even when their repository metadata cannot verify them
      --autoRegister string                Register unknown MACs on their first /booty.ipxe or /ignition.json fetch (bluefin: their first UEFI HTTP Boot netboot UKI fetch, which ProxyDHCP then offers every unknown x86-64 HTTP Boot client) as this OS (flatcar, coreos or bluefin) instead of sending them to the brig; empty disables
      --autopilot string                   Self-healing upgrades: 'off', 'guard' (health gate, retry, downgrade to lastGood and fleet hold for every OS; rollout order unchanged) or 'full' (guard plus the Bluefin canary-serial rollout, timeout/retry, quarantine and skip-to-next). Reboots go through kured when it runs in the cluster, else --rebootSSHKey, else the Kubernetes API (default "off")
      --autopilotCooldown duration         Minimum time between the starts of two waves of the same OS (the non-canary hosts move together into the newest soaked release). 0 disables the cooldown
      --autopilotDrainTimeout duration     How long the API actuator keeps retrying evictions refused by a PodDisruptionBudget before it gives up on draining a node (default 10m0s)
      --autopilotHealthWindow duration     Health-gate window: how long a host has, from its observed reboot (first kernel/UKI fetch), to come back Ready with its workloads healthy; the node must stay Ready for 5m or half the window, whichever is shorter (default 15m0s)
      --autopilotImage string              Image of the reboot Pods (must be Booty's own, it runs 'booty node-reboot'); defaults to the BOOTY_IMAGE environment variable, and the API actuator refuses to run without one
      --autopilotIssueTitlePrefix string   Text put in front of the title of every issue --autopilotIssues files and of its comments' first line, verbatim (e.g. '[autopilot test] ' while testing against your own repository); the dedupe marker is unchanged
      --autopilotIssues string             GitHub owner/repo the autopilot files quarantined Bluefin releases to as issues (redacted report, deduplicated per release by a hidden marker; a second machine comments), with --githubToken (issues:write). Empty keeps the reports on disk only; try a fork or scratch repository before projectbluefin/server
      --autopilotNamespace string          Namespace the autopilot's API actuator creates reboot Pods in; defaults to Booty's own (the POD_NAMESPACE downward-API variable), else kube-system
      --autopilotRetryAfter duration       How long a release that a host rolled back from stays in TIMEOUT before the autopilot retries it once (on the Bluefin canary, or the same Flatcar/CoreOS host) (default 1h0m0s)
      --autopilotSoak duration             How long a release must have been healthy on a canary (a host with canary: true) before non-canary hosts may move to it; without a canary registered for an OS it has no effect on that OS. 0 disables the soak
      --bluefinKeyring string              OpenPGP public keyring (binary as for gpgv --keyring, or armored) that must have signed a Bluefin release's SHA256SUMS (SHA256SUMS.gpg); the sync fails closed when set. Empty trusts SHA256SUMS from the release as-is
      --bluefinOCI string                  Fetch the Bluefin Server release files from this ORAS OCI artifact repository instead of GitHub releases (e.g. ghcr.io/projectbluefin/bluefin-server; tags <version> and latest; http://host:port/repo for a plain-HTTP registry). --githubToken is sent to ghcr.io only
      --bluefinRepo string                 GitHub repository whose v<version> releases provide the Bluefin Server netboot UKI, OS DDI and sysexts (default "projectbluefin/server")
      --bluefinVersion string              Pin a specific Bluefin Server release (e.g. 26.09.747, tag v26.09.747). When empty, tracks the newest v<version> release (or the OCI artifact's latest tag)
      --builtin string                     Comma separated builtin Ignition fragments merged into every registered host's config (hostname, update, booted, sshkeys, health, wol), or 'none' to serve the user config as-is (default "hostname,update,booted,sshkeys,health,wol")
      --clusterCADir string                Bring-your-own cluster CA directory for --controlPlane=managed (kubeadm: ca.crt/ca.key; k0s: also sa.key, sa.pub, etcd/ca.crt, etcd/ca.key), read-only; empty generates one under --dataDir/cluster/pki
      --clusterDistribution string         Kubernetes distribution of the cluster Booty provisions: 'kubeadm' or 'k0s' (Bluefin hosts: k0s, or kubeadm workers of an external cluster) (default "kubeadm")
      --cni string                         Network plugin installed from the first control plane: 'cilium', 'calico', 'flannel' or 'none' (default "cilium")
      --cniRelease string                  Overrides the pinned release of the selected --cni (--cniVersion keeps meaning containernetworking/plugins)
      --cniVersion string                  containernetworking/plugins release installed by the kubeadm-worker profile (default "v1.1.1")
      --containerdDisk string              Block device PXE-booted nodes format (ext4, wiped on every boot) for their image cache: /var/lib/containerd under kubeadm, /var/lib/k0s on a k0s worker and /var/lib/k0s/containerd on a k0s controller, e.g. /dev/sda; Bluefin kubeadm workers format it ext4 once if it is blank (no filesystem, no partition table), never wipe one that carries anything, refuse their own stateDisk/installDisk, and mount it at /var/lib/containerd-disk with containerd in its bluefin/ directory; empty keeps it on the root filesystem
      --controlPlane string                Who runs the control plane: 'external' (join-only, today's behaviour) or 'managed' (Booty generates the cluster CA under --dataDir/cluster/ and renders the role: control-plane host) (default "external")
      --controlPlaneDisk string            Block device the managed control plane formats once (ext4, label booty-cp, never wiped) and keeps its state on (kubeadm: /etc/kubernetes, /var/lib/etcd, /var/lib/kubelet; k0s: /var/lib/k0s), e.g. /dev/vda; required for a role: control-plane host on PXE-booted Flatcar/CoreOS (Bluefin installs to disk) and must differ from --containerdDisk
      --controlPlaneEndpoint string        host[:port] every node uses for the API server (VIP or DNS name for HA); with --controlPlane=managed it defaults to the single role: control-plane host's IP
      --coreOSArchitecture string          Architecture to use for CoreOS downloads (default "x86_64")
      --coreOSChannel string               CoreOS channel to look for updates, or 'none' to neither track nor download CoreOS (its hosts are refused) (default "stable")
      --coreOSStreamsURL string            Fedora CoreOS streams JSON URL template (%s = channel) (default "https://builds.coreos.fedoraproject.org/streams/%s.json")
      --crictlVersion string               cri-tools release installed by the kubeadm-worker profile; defaults to the --k8sVersion minor with patch 0 (cri-tools tags once per minor, e.g. v1.34.0)
      --dataDir string                     Directory to store stateful data (default "/data")
      --debug                              Enable debug logging
      --doInstallClearOn string            When to clear a host's pending doInstall: 'ignition' (first Ignition fetch; Bluefin: the bluefin-node.ign carrying the install unit), 'booted' (only on POST /booted from the installed system; Bluefin behaves like 'next-boot') or 'next-boot' (Bluefin: the first netboot UKI fetch at least --installMinDuration after the install boot was served; other OSes behave like 'booted'). A Bluefin host whose doInstall clears becomes mode installed (default "ignition")
      --efiBootloader string               iPXE build ProxyDHCP hands x86-64 UEFI clients: 'ipxe' (ipxe.efi, iPXE's own NIC drivers) or 'snponly' (snponly.efi, the firmware's network stack); HTTP Boot clients get the matching signed shim (ipxe-shimx64.efi / snponly-shimx64.efi) (default "ipxe")
      --fedoraGrubVersion string           Fedora grub2-efi-x64 package version (e.g. 2.12-64.fc44) whose grubx64.efi is served next to the Fedora shim; sha256 pinned in code for the default (default "2.12-64.fc44")
      --fedoraMirrorURL string             Fedora mirror root (…/pub/fedora/linux) for shim/GRUB RPMs (default "https://dl.fedoraproject.org/pub/fedora/linux")
      --fedoraReleases string              Comma-separated Fedora releases to search for RPMs, newest first (default: derived from --fedoraGrubVersion)
      --fedoraShimVersion string           Fedora shim-x64 package version (e.g. 16.1-7) whose shimx64.efi Secure-Boot CoreOS hosts chain through; sha256 pinned in code for the default (default "16.1-7")
      --flatcarArchitecture string         Architecture to use for the Flatcar downloads (default "amd64")
      --flatcarChannel string              Flatcar channel to look for updates, or 'none' to neither track nor download Flatcar (its hosts are refused) (default "stable")
      --flatcarVersion string              Pin a specific Flatcar version (e.g. 3815.2.0). When empty, tracks the latest version on the configured channel
      --githubAPIURL string                GitHub REST API base URL (GitHub Enterprise: https://host/api/v3) (default "https://api.github.com")
      --githubDownloadURL string           Base URL GitHub release assets are downloaded from (default "https://github.com")
      --githubToken string                 GitHub token sent as a bearer token to the releases API (raises the unauthenticated 60 requests/hour limit; no scopes needed) and, with --autopilotIssues, used to file autopilot reports as issues (needs issues:write on that repository)
  -h, --help                               help for booty
      --hostnameTemplate string            Go template for auto-registered hostnames; fields: .MAC, .MACSuffix (last 3 bytes hex), .MACFlat (12 hex), .IP (default "node-{{ .MACSuffix }}")
      --httpPort int                       Port to use for the HTTP server (default 8080)
      --installMinDuration duration        Minimum time between a Bluefin install boot and the netboot UKI fetch that counts as 'install finished' for --doInstallClearOn=next-boot/booted; earlier fetches install again (default 3m0s)
      --joinString string                  The kubeadm join string to use to auto-join to a K8s cluster (kubeadm join 192.168.1.10:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:SHA_HASH)
      --joinStringFile string              File holding the kubeadm join string (e.g. a mounted Secret); re-read on every render and wins over --joinString
      --joinTokenTTL duration              Lifetime of bootstrap tokens minted through the Kubernetes API (--kubeadmJoin=auto, k0s worker tokens); expired ones are deleted on the --updateSchedule tick (default 1h0m0s)
      --k0sTokenFile string                File holding a pre-made k0s worker join token for an external k0s control plane; the fallback when --kubeconfig is not set or minting fails
      --k0sVersion string                  k0s release Flatcar/CoreOS hosts download to /opt/bin/k0s under --clusterDistribution=k0s (sha256-verified; the default is pinned in code and matches Bluefin Server's /usr/bin/k0s, other versions are checked against the release's sha256sums.txt) (default "v1.36.4+k0s.0")
      --k8sVersion string                  Kubernetes release installed by the kubeadm-worker profile (default "v1.34.3")
      --kubeadmJoin string                 Where the kubeadm join string comes from: 'static' (--joinString/--joinStringFile) or 'auto' (mint a short-lived bootstrap token through the Kubernetes API on every boot: in-cluster, via --kubeconfig, or with Booty's own CA when --controlPlane=managed) (default "static")
      --kubeconfig string                  Kubeconfig for talking to an external control plane from outside the cluster: minting join tokens (kubeadm with --kubeadmJoin=auto, k0s worker tokens always) and the autopilot's cluster client
      --kubeletUnitsURL string             Base URL the kubeadm-worker profile fetches kubelet/kubelet.service and kubeadm/10-kubeadm.conf from (pin or mirror it) (default "https://raw.githubusercontent.com/kubernetes/release/master/cmd/krel/templates/latest")
      --ociGC                              No-op kept so an old command line still starts (default true) (DEPRECATED: the OCI image cache was removed; the flag does nothing and goes away in the next release)
      --ociGCEmpty                         No-op kept so an old command line still starts (DEPRECATED: the OCI image cache was removed; the flag does nothing and goes away in the next release)
      --podCIDR string                     Pod network CIDR of the cluster (default "10.244.0.0/16")
      --profile string                     Node profile appended to the builtin Ignition fragment for flatcar/coreos hosts: '' or 'kubeadm-worker' (CNI plugins, kubeadm/kubelet/kubectl/crictl, kubelet units, kubeadm join on every boot); bluefin workers get the same join through the kubeadm sysext in their node Ignition
      --proxyDHCP                          EXPERIMENTAL: answer PXE clients as a ProxyDHCP server (UDP 67 + 4011) so the network's DHCP server needs no next-server/filename
      --proxyDHCPListen string             IP or interface name the ProxyDHCP server binds to (default all interfaces)
      --proxyDHCPRelay                     Answer relayed PXE requests (giaddr set) on the ProxyDHCP server
      --rebootSSHKey string                Private key for the SSH actuator (as core with sudo on every OS; host keys pinned on first use in --dataDir/autopilot/known_hosts); reboots go through kured when it runs in the cluster, else this key, else the Kubernetes API; empty disables it
      --secureBoot                         Answer UEFI HTTP Boot clients (Secure Boot firmware) over ProxyDHCP with a Microsoft-signed iPXE shim and sync the signed boot artefacts into --dataDir/secureboot/; requires --proxyDHCP. Off: HTTP Boot clients other than Bluefin hosts (always offered their netboot UKI) are ignored and nothing is downloaded
      --secureBootIPXEShimVersion string   ipxe/shim release providing the Microsoft-signed ipxe-shimx64.efi (sha256 pinned in code for the default, the release's asset digest otherwise) (default "ipxe-16.1")
      --secureBootIPXEVersion string       ipxe/ipxe release whose ipxeboot.tar.gz provides the iPXE-CA-signed x86_64-sb/ipxe.efi and snponly.efi (sha256 pinned in code for the default) (default "v2.0.0")
      --secureBootTrusted string           Comma separated Secure Boot CAs the fleet's firmware db trusts besides the implied 'microsoft': 'flatcar' asserts the Flatcar CA (served at /boot/secureboot/flatcar-ca.der) is enrolled so Flatcar kernels may be booted under Secure Boot
      --serverHttpPort int                 HTTP port clients use to reach Booty when it differs from --httpPort (port mapping); 0 means same as --httpPort
      --serverIP string                    IP address that clients can connect to; autodetected from the default route when empty (set explicitly behind a VIP/NAT)
      --serviceCIDR string                 Service network CIDR of the cluster (default "10.96.0.0/12")
      --sshAuthorizedKeys strings          SSH public key added to the 'core' user by the sshkeys builtin (repeatable)
      --sshAuthorizedKeysFile string       File with SSH public keys (one per line) added to the 'core' user by the sshkeys builtin
      --tftpBlockSize int                  TFTP block size to negotiate with clients (default 1468)
      --tftpPort int                       UDP port to use for the TFTP server (default 69)
      --updateSchedule string              Cron schedule for the Flatcar/CoreOS/Bluefin version checks (default "*/5 * * * *")
      --webDir string                      Directory with the built Web UI, used when no UI is embedded in the binary (default "./web/dist")

Use "booty [command] --help" for more information about a command.
```

Every flag can also be set through the environment as `BOOTY_<FLAGNAME>` (upper-cased, e.g. `BOOTY_SERVERIP=192.168.1.10`, `BOOTY_JOINSTRING=...`, `BOOTY_PROFILE=kubeadm-worker`). The older `IGNITION_FILE`, `HARDWARE_MAP` and `FLATCAR_VERSION_PIN` names still work.

`--serverIP` is the address booting machines use to reach Booty. When left empty it is autodetected at startup from the default route (logged as `serverIP autodetected`). Set it explicitly whenever that address is not what clients should use -- behind a MetalLB/keepalived VIP, a NAT or a hostPort mapping the node IP is the wrong answer. `--serverHttpPort` only matters when clients reach Booty on a different port than it listens on (port mapping); it defaults to `--httpPort`, and `:80` is omitted from generated URLs.

## Upstream endpoints and startup validation

The endpoint flags relocate Booty's upstreams: `--githubAPIURL` (GitHub REST API base, e.g. a GitHub Enterprise `https://host/api/v3`), `--githubDownloadURL` (base URL release assets are downloaded from), `--fedoraMirrorURL` (root of a Fedora mirror tree, `…/pub/fedora/linux`) and `--coreOSStreamsURL` (streams JSON template, one `%s` for the channel). `--fedoraReleases` lists the Fedora releases searched for shim/GRUB RPMs, newest first; when empty it is derived from the `.fcNN` suffix of `--fedoraGrubVersion` as `NN+1` and `NN`, so it moves along with the GRUB pin.

Startup validates these settings and exits on malformed values: the Flatcar/CoreOS download URL templates must carry exactly three `%s` verbs, `--coreOSStreamsURL` exactly one (and no other verbs), the base URLs must parse as `http(s)` URLs, and `--fedoraReleases` may only list numbers. `--tftpBlockSize` must be in 512..65464 (the TFTP option's legal range); `0` is accepted and keeps the library default (512).

## Downloads

Release downloads retry three times with exponential backoff and resume partial files over HTTP Range, so a restarted sync does not start a large DDI from zero. Temporary files are named `<name>.*.tmp` next to their target and swept at startup. GitHub release fetches use ETag/`If-None-Match`, and a `304 Not Modified` does not count against the API rate limit. Bluefin sysext zstd decompression is capped at 16 GiB.

## Configuration view and editor

`GET /config` shows the running configuration and the Butane templates in play, so you do not have to guess which flag, environment variable or default a Booty instance ended up with:

```json
{"settings":[{"key":"serverIP","value":"192.168.1.10","source":"flag","redacted":false}, ...],
 "templates":{"default":{"name":"config/ignition.yaml","source":"file","writable":true},
              "hosts":[{"mac":"aa:bb:cc:dd:ee:01","hostname":"n1","name":"config/n1.yaml"}]}}
```

* `settings` lists every flag, sorted by key, with its effective value rendered as a string and its `source`: `flag` when it was passed on the command line, `env` when `BOOTY_<FLAG>` (or one of the legacy `IGNITION_FILE`/`HARDWARE_MAP`/`FLATCAR_VERSION_PIN` names) is set, otherwise `default`. Values Booty computes itself -- the autodetected `serverIP`, the derived `serverHttpPort`, the build stamp -- count as `default`. `joinString` and `githubToken` are always `redacted:true` and shown as `••••` when non-empty (empty stays empty); `joinStringFile` is a path, not the secret, and is shown as-is.
* `templates.default` is `--ignitionFile`; `source` is `embedded` while the file does not exist and Booty renders its built-in minimal template, `file` otherwise. `templates.hosts` lists registered hosts with their own `ignitionFile`.
* `writable` says whether saving the template from the UI would work. Booty probes the file (opened for writing and closed, never modified) or, for a missing file, its directory. When the probe fails the response carries `readOnlyReason`, e.g. `"/data/config/ignition.yaml is read-only (read-only file system)"`.

The template editor behind it:

| Endpoint | Does |
|---|---|
| `GET /config/template?name=config/ignition.yaml` | Returns `{name, source, writable, readOnlyReason?, content}`. `name` defaults to `--ignitionFile`; it must be a relative path inside `--dataDir` ending in `.yaml`, `.yml` or `.bu`, and may not name `hardware.json`, the pin file or temp files (400). A missing file is a 404, except for the default template, which returns the embedded Butane with `source:"embedded"`. |
| `POST /config/template/validate` | Body `{"name","content","mac"?}`. Renders the template exactly like a boot would and translates it with Butane; answers `{"ok","ignition","entries":[{"kind":"warning|error","message"}]}` with HTTP 200 even when the template is broken (`ok:false`). With `mac` the registered host's hostname/image are used (404 if unknown); without it a dummy host (`example`, `00:00:00:00:00:00`) stands in. `{{ .JoinString }}` is the static join string; with `--kubeadmJoin=auto` it is the placeholder `kubeadm join <auto>` -- validation never mints a token. |
| `PUT /config/template` | Body `{"name","content"}`. Validates with the dummy host first (`400 {"error":"template does not validate","entries":[...]}`), refuses read-only targets (`409 {"error":"template is read-only","reason":"..."}`), then writes the file atomically inside `--dataDir`, logs `Ignition template saved via API` with name and size, and answers like the GET. Never writes outside `--dataDir`. |

**GitOps and ConfigMap mounts.** If `/data/config/` is a Kubernetes ConfigMap (as in [examples/k8s.yaml](../examples/k8s.yaml)) or otherwise mounted read-only, the view still works but shows `writable:false` with the reason, the UI disables saving, and a `PUT` is answered with 409 -- edit the source of truth (Git, the ConfigMap) instead. Validation is always available.

**Trust model.** The editor has no authentication, like the rest of Booty (see [Trust model](trust-model.md#trust-model)): anyone who can reach the HTTP port can read the effective configuration (secrets redacted) and, on a writable data directory, replace the Butane template every host boots with. Keep the port on the boot VLAN or mount the template read-only if that is not acceptable.
