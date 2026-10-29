# Power

Per-host power tracking and operator power actions (Wake-on-LAN, reboot, shutdown) without a BMC.


Booty tracks whether each host is up and gives the operator three buttons -- **Power on** (Wake-on-LAN), **Reboot** and **Shutdown** -- without a BMC. The design is in [docs/plans/2026-09-30-power.md](plans/2026-09-30-power.md); nothing here needs the autopilot to be on.

### State model

Every host carries a `power` block (`/hosts`, `/booty.json`, `GET /power`): `{state, since, reason, request, requestedBy, requestedAt, lastSeen, cordoned, probe:{ok, at, method}}`. The tracker recomputes it every 30 s from the signals Booty already records and an active probe:

| State | Means | Entered from |
|---|---|---|
| `unknown` | no IP to probe and nothing seen for 15 min (or never) | start-up, a quiet host without an IP |
| `off` | a requested shutdown was satisfied (the host stopped answering), or a wake-up that did not wake within 10 min | `shutting-down`, `powering-on` |
| `powering-on` | a magic packet was sent; waiting up to 10 min for the kernel fetch | *Power on* |
| `booting` | kernel/UKI (or Ignition) fetched | `/booty.ipxe`, the Bluefin UKI (from the host's own address only, see [the health gate](autopilot.md#the-health-gate)), `/ignition.json`, `bluefin-node.ign` |
| `up` | `/booted`, `/health` or `/update-check` arrived, the node is `Ready`, or the probe answers | any |
| `draining` | cordon + drain in progress before a reboot or shutdown | *Reboot*, *Shutdown* |
| `rebooting` | an operator reboot (10 min window) or an autopilot attempt waiting for its reboot | *Reboot*, the controller's `RebootWanted` |
| `shutting-down` | `systemctl poweroff` issued; the first failed probe makes it `off`, 5 min of answering makes it `up` again ("did not shut down") | *Shutdown* |
| `unreachable` | the probe fails and nothing has been seen for 15 min | `up`, `booting`, an expired reboot window |

The **probe** is a TCP connect to the host's IP on 22, then 10250, 3 s each, only for hosts with an IP; a host with neither sshd nor a kubelet still stays `up` through its 10-minute `/update-check`. `lastSeen` is the last proof of life (signal, Node Ready or a successful probe). Requests are **expectations with a window** (10 min power-on and reboot, 5 min shutdown) resolved from the signals, so nothing shows `rebooting` forever: an unobserved reboot ends `up` or `unreachable` with an event; `POST /power/{mac}/cancel` drops a stuck one at once. The record is rewritten only when something other than the probe time changed (or it moved by a minute), so a quiet fleet does not churn `hardware.json`. Events (kind `power`, the MAC as a field, no hostnames/IPs/MACs in the text) go into the autopilot's ring when a controller runs and always into the tracker's own ring of 200 (`GET /power`); the Autopilot page shows both.

### Actions

`POST /power/{mac}/{on|reboot|shutdown|cancel}` with an optional body `{"reason": "...", "force": false, "drain": true}` answers `202` with the new `power` block (`200` for cancel), `404` for an unregistered MAC, `409` with `{"error": "...", "power": {...}}` when it refuses: another action in flight (the same action within its window is idempotent), a guard, a state the action is not allowed from, or no actuator.

* **Power on** (from `off`, `unreachable`, `unknown`): the magic packet (6 × `0xFF` + the MAC × 16) to UDP 9 on the directed broadcast of every non-loopback IPv4 interface Booty has, `255.255.255.255`, and the host's own IP when known (the switch may still have the MAC), three rounds a second apart. Needs no cluster and no actuator. Requirements on the host's side: same L2 segment as one of Booty's interfaces (or a unicast path to its last IP), the NIC armed (`WakeOnLan=magic`, the `wol` builtin does it; `ethtool <if>` should say `Wake-on: g`), and WoL enabled in the firmware setup ("Wake on LAN", "Power on by PCI-E", "ErP off"). Booty cannot see the firmware setting: if nothing fetches a kernel within 10 min the host goes back to `off` with a "did not wake" event. In a container, only the unicast and limited-broadcast sends reach the LAN unless it runs with host networking.
* **Reboot** (from `up`): refused for `role: control-plane` unless `force`, when the autopilot has an episode in flight on the host (`autopilot is driving this host`), and when there is no operator actuator (`no actuator: configure --rebootSSHKey or run Booty in the cluster`). Otherwise, in the background: cordon and drain through the Kubernetes client when `drain` (default true) and one is reachable (`API.Drain`: DaemonSet and mirror pods skipped, PodDisruptionBudgets honoured until `--autopilotDrainTimeout`, the node resolved by hostname and confirmed by the DMI product UUID), then `systemctl reboot` over SSH (`--rebootSSHKey`, as `core` with sudo on every OS) or the privileged `booty-node-reboot-<node>` Pod (`/booty node-reboot`, SIGRTMIN+5). **Operator reboots never go through kured**: its sentinel means "in the next window", a button means now. `cordoned: true` while Booty owes the node an uncordon; `POST /booted` from the rebooted host uncordons it. If no `/booted` arrives within 15 min the node is left cordoned and an event says so; a late `/booted` still uncordons. A drain or actuator failure before the reboot was issued reverts the state, uncordons and emits an event.
* **Shutdown** (from `up`): the same guards plus a refusal to take down the **last Ready worker** (counted from the nodes when a cluster is reachable -- Ready, schedulable, not control-plane -- else from the registered workers that are `up`) unless `force`. Drain as above, then `systemctl poweroff` over SSH or the Pod with `/booty node-reboot --poweroff` (SIGRTMIN+4, `poweroff.target`). The node **stays cordoned while off**, on purpose; it is uncordoned when the host reports `/booted` again. While `request: shutdown` and the state is `shutting-down`/`off`, **the autopilot excludes the host**: no episode starts, nothing is `hung` or `needs-hands`, and `lastGood` does not wait for it; the first kernel fetch clears the request and the host is gated like any other. `/update-check` keeps answering normally when it returns.
* **Cancel**: clears a stuck `powering-on`/`draining`/`rebooting`/`shutting-down` expectation; the state is recomputed from the last probe and `lastSeen`. A background action that is still running finishes but no longer writes state.

`GET /power` returns `{hosts: {<mac>: power}, events, capabilities: {wol: true, actuator: "ssh"|"api"|"none"}, summary: {up, off, unreachable, inFlight}}`; the `actuator` is the operator one (SSH when `--rebootSSHKey` is set, the API when a cluster client is configured and reachable -- with `--autopilot=off` too, the client and chooser are built regardless -- else `none`) and is what the buttons' enablement reads. `GET /info` gains `"power": {up, off, unreachable, inFlight}`.

### UI

The Hosts table shows a power badge (coloured dot, state, since when; the tooltip carries the reason, the request, the probe and whether Booty cordoned the node) next to the hostname and a **Power** column with the three buttons, enabled by state (*Power on* from `off`/`unreachable`/`unknown`, *Reboot* and *Shutdown* from `up`) and otherwise disabled with the reason in the tooltip (`host is rebooting`, `autopilot is driving this host`, `no actuator: …`). Clicking opens an inline confirm with a reason field and -- only when the server would refuse -- a *Force* checkbox (control-plane host; last worker that is up). A 409 is shown on the row with the state the server answered with. The host form repeats the badge read-only. The Overview's Fleet card shows `6 up · 0 off · 0 unreachable` (and how many are in flight); the Autopilot timeline shows `power` events, also when the autopilot is off.

### No authentication, by design

Like `/register`, `/flatcar/pin` and `/autopilot/.../clear`, the power endpoints need nothing but reach to the port: anyone on the boot VLAN can wake, reboot or shut down a registered host. That is the LAN boundary Booty already trusts (see [Trust model](trust-model.md#trust-model)); keep the port off networks you do not. `requestedBy` records the caller's address on the host record (not in events or reports).
