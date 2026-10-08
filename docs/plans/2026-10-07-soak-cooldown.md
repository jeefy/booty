# Soak and cooldown: paced rollouts

Booty used to move the whole fleet to every release the moment it landed:
under `--autopilot=full` one Bluefin host at a time, a few hours after each
of projectbluefin/server's one or two releases a day; under `guard` (and
for Flatcar and CoreOS) kured-wide. Every host rebooted once or twice a day.
This plan keeps a **canary** that still reboots into every release, and
adds two clocks for everyone else: a **soak** a release must spend healthy
on a canary before the fleet may adopt it, and a **cooldown** between the
**waves** in which the fleet moves. Both default to `0`, which is exactly
the behaviour of v1.0.0.

## Decisions (user, 2026-10-07)

| Topic | Decision |
|---|---|
| Cooldown semantics | **Fleet wave**, not per host: the non-canary hosts of an OS move together (serially under `full`/Bluefin, kured-ordered otherwise) into one release, and a new wave starts at most once per `--autopilotCooldown`, counted from the previous wave's start. Predictable ("one wave per 48 h"), and every host of the wave lands on the same release. |
| Scope | Every OS and both modes (`guard` and `full`). The hold and pin machinery is OS-agnostic; only the reboot driver differs (actuator and serial under `full`/Bluefin, pins + kured elsewhere). |
| Defaults | `--autopilotSoak=0 --autopilotCooldown=0`: no pacing, v1.0.0 behaviour. The homelab runs `24h`/`48h`. |
| Skip-ahead | A wave targets the **newest** soaked, non-bad release, not the next one in sequence: three releases in 48 h cost one reboot per host. Releases the fleet never moved to are marked `skipped`. |
| Canary | `canary: true` hosts (any OS, already a host field). Exempt from both clocks: they follow `current` (the newest non-bad release) as today. Without a canary registered for an OS, soak has no effect on that OS (warned once per release); cooldown still applies. |
| Safety paths | Retries, rollbacks, TIMEOUT retries, operator `targetVersion` pins and the Power buttons ignore both clocks. A release going bad mid-wave aborts the wave; its hosts roll back through the existing machinery. |

## Facts the design rests on

- `pkg/versions/releases.go`: `FleetTarget(os)` is `current` unless held
  (`HoldFleetTarget`) or `SerialRollout(os)` is on, in which case it is
  `lastGood` whenever `current` differs from it, synchronously with the
  release landing. The controller re-applies holds from `state.json` on
  start. **Nothing a host without a `targetVersion` does can move it past
  the fleet target**, so the hold alone keeps non-canaries still; pins
  (`targetVersion`, `Controller.pin`) are how individual hosts move ahead.
- `pkg/autopilot/controller/rollout.go`: `driveRollouts` (full, Bluefin)
  starts one episode at a time in `canaryOrder` (canary → workers → control
  planes) into `rolloutCandidate` (`current`, else the newest cached
  non-bad release newer than `lastGood`). `advanceLastGood` moves `lastGood`
  when `fleetHealthyOn`: every booted host of the OS is healthy on R.
- Under `guard`, the controller never initiates a rollout reboot; it gates
  the reboots it **observes** (`observeKernelFetch` starts an episode when a
  host boots a release it has not passed on) and pins only for retries and
  rollbacks. kured (or the host's own update timer) does the rebooting from
  `/update-check`, which compares `running` with `targetVersion` else the
  fleet target.
- `Release.Healthy` lists the MACs that passed the gate; `healthy()` in
  `episode.go` appends to it. There is no timestamp of the first pass yet.
- `state.json` is the controller's only persistence; `OSState` already
  carries `Held`/`HeldSince`. Tests drive the controller with a fake clock
  (`Options.Now`, `harness.advance`) and a fake fleet, so 48 h is a call.

## Out of scope

Per-host cooldown (a host that was retried an hour ago is not protected
from a wave; waves are far apart); maintenance windows (time of day, days
of week; kured has them); a UI to edit the two durations (flags only);
changing kured's own ordering or lock; pacing for installed-mode Bluefin.

## Definitions

- **Paced** (per OS): `--autopilotSoak > 0 || --autopilotCooldown > 0`.
  Bluefin under `full` keeps its serial rollout whether paced or not.
- **Canaries** (per OS): the registered, not-excluded hosts of that OS with
  `canary: true`.
- **Soaked** (per release R of an OS): any of
  - soak is `0`;
  - the OS has no canaries (warned once per release, event kind `release`);
  - `R.firstHealthyAt` is set, `now − firstHealthyAt ≥ soak`, **and** at
    least one canary still counts as healthy on R: `healthyOn(canary) == R`
    and its last health report (if any) names `running == R` with no failed
    units. A canary that moved on to R+1 keeps R soaked only if another
    canary is still on it; a canary that fell over un-soaks it.
- **Wave** (per OS): one pass of the non-canary hosts into one release.
  `OSState.wave = {release, startedAt, endedAt, outcome}`; `outcome` is
  `rolling`, `done` or `aborted`. A wave is *open* while `endedAt` is zero.
- **Wave candidate** (per OS): the newest cached release that is newer than
  `lastGood`, not `Bad()` (TIMEOUT/quarantined), not `Failing`, and soaked;
  `""` when there is none.
- **Eligible for a wave**: a non-canary host of the OS that is not
  excluded, has no active episode, and is not healthy on the wave's
  release (nor `needs-hands` on it).

## Behaviour

### Fleet target

For a paced OS (and Bluefin under `full`, as today) `desiredHold` is
`lastGood` whenever `current != lastGood`, and `New` turns
`Fleet.SerialRollout(os)` on for it so the hold is in force the instant a
release lands. Unpaced OSes keep today's rule (held only while `current` is
bad or a host is failing on it).

### Canaries

- `full` + Bluefin: `driveRollouts` keeps doing what it does, but only for
  canaries when the OS is paced: candidate `rolloutCandidate(os)`, one
  active episode per OS at a time, `startEpisode` + `pin` + `requestReboot`.
- Everything else, paced: on every tick, a canary with no active episode
  whose `healthyOn` is not `rolloutCandidate(os)` is pinned to it
  (`pin`). kured / the update timer reboots it; `observeKernelFetch` opens
  the gate. Nothing else changes for it.
- `firstHealthyAt`: `healthy()` sets `r.FirstHealthyAt = now` the first
  time it appends to `r.Healthy` (any host, not only canaries: an operator
  who pins a worker ahead starts the clock too). Persisted; `Status` also
  computes `soaked` per release for the UI.

### Waves

`driveWaves(os)` runs every tick, after the canary step, for each paced OS:

1. If a wave is open: if its release is `Bad()`, end it (`outcome:
   aborted`, event), and `unpin` every controller-pinned host whose
   `targetVersion` is the wave's release and that has no active episode
   (they fall back to the held fleet target, `lastGood`; a host already
   mid-episode finishes through the normal rollback path). Otherwise, if no
   host is eligible any more, end it (`outcome: done`, event). Otherwise
   drive it (step 4) and stop.
2. If cooldown is on and `now − wave.startedAt < cooldown` for the last
   wave, stop (`nextWaveAt` is shown).
3. Compute the wave candidate; `""` means stop. Start the wave: `wave =
   {release, startedAt: now, outcome: rolling}`, event `wave started into
   <R> (<n> host(s)); next wave not before <t>`.
4. Drive:
   - `full` + Bluefin: if any host of the OS has an active episode, stop
     (serial, shared with the canary). Else take the first eligible host in
     `canaryOrder` and `startEpisode` + `pin` + `takeBaseline` +
     `requestReboot` exactly as the rollout does today.
   - Otherwise: `pin` every eligible host to the wave's release (all at
     once; kured orders the reboots). Later ticks re-pin any eligible host
     that lost its pin (an operator cleared it) only if the wave is open.

When soak and cooldown are both `0` the wave candidate equals
`rolloutCandidate` and the cooldown check never stops anything, but to keep
v1.0.0 byte-identical **unpaced OSes do not run `driveWaves` at all**:
under `full` Bluefin's `driveRollouts` keeps covering every host, and under
`guard` nothing is pinned for rollouts.

### lastGood and skipped releases

`fleetHealthyOn(os, v)` counts a **canary** as healthy on `v` when its
`healthyOn` is `v` **or a newer release that is not `Bad()`**; non-canaries
must be on `v` exactly, as today. Without this a canary already on R+1
would hold `lastGood` at R−1 forever.

When `lastGood` advances to `v`, every release of the OS with state
`rolling`, version older than `v`, not `Bad()`, not `Failing` and with no
active episode on it becomes `skipped` (`ReleaseSkipped`, new state, event
`release skipped: the fleet moved to <v> without it`). `compactReleases`
treats `skipped` like `good` (foldable, counts toward the 50 cap);
`Status` lists it with the good history (bounded) rather than as live.
`Bad()` is unchanged, so a skipped release could still be targeted by an
operator pin.

### Events

All kind `release` or `fleet`, no identifiers in the text (MAC is a field):

- `release soaked: healthy on a canary for <soak>; eligible for the next wave`
  (once per release, when it first becomes soaked)
- `no canary registered for <os>; --autopilotSoak has no effect on it` (once per release)
- `wave started into <R> (<n> host(s)); next wave not before <RFC3339>`
- `wave done: every host is on <R>` / `wave aborted: <R> is <timeout|quarantined>`
- `release skipped: the fleet moved to <v> without it`

## Configuration

| Flag | Default | Meaning |
|---|---|---|
| `--autopilotSoak` | `0` | How long a release must have been healthy on a canary before non-canary hosts may move to it. `0` disables the soak. |
| `--autopilotCooldown` | `0` | Minimum time between the starts of two waves of the same OS. `0` disables the cooldown. |

Both are durations, `BOOTY_AUTOPILOTSOAK`/`BOOTY_AUTOPILOTCOOLDOWN` through
the environment like every flag, validated `≥ 0` (negative is a start-up
error), ignored with `--autopilot=off`. `pkg/config`: `AutopilotSoak`,
`AutopilotCooldown`, `DefaultAutopilotSoak`, `DefaultAutopilotCooldown`;
`autopilot.Settings.Soak/Cooldown` → `controller.Options.Soak/Cooldown`.
`LogStatus` names both when non-zero.

## State and API

`state.json` additions (all `omitempty`/`omitzero`, so an unpaced
deployment's file does not change):

```json
"os": {"bluefin": {"releases": {"26.10.900": {"firstHealthyAt": "2026-10-07T10:15:00Z", ...}},
                   "wave": {"release": "26.10.880", "startedAt": "...", "endedAt": "...", "outcome": "done"}}}
```

`GET /autopilot` top level gains `"soak"` and `"cooldown"` (duration
strings like `healthWindow`, present when non-zero). Per OS
(`os.<name>`): `"wave"` (as above, omitted when there never was one),
`"nextWaveAt"` (RFC3339, only while the cooldown blocks the next wave),
`"canaries"` (count). Per release: `"firstHealthyAt"` and `"soaked"`
(computed, like `cached`). `ReleaseSkipped = "skipped"` appears in
`state`. `/info.autopilot` is unchanged.

## UI

Autopilot page (`web/src/views/AutopilotView.vue`):

- Header shows `soak 24h · cooldown 48h` next to the health window when set.
- Per OS card: a **Wave** line — `wave into 26.10.880 · 2/3 hosts · started
  3 h ago`, or `last wave done 20 h ago · next wave in 28 h`, or `aborted:
  26.10.880 quarantined`.
- Release rows: a `soaking 13h / 24h` badge on a rolling release that is
  not soaked yet, `soaked` once it is; `skipped` rendered muted like good,
  folded into the *Show N good releases* toggle.
- Hosts table already shows the `canary` badge; nothing new.

## Tests (`pkg/autopilot/controller`)

Existing tests run unchanged with `Soak: 0, Cooldown: 0`. New, with the
fake clock:

1. **full/Bluefin paced**: canary A, workers B and C; soak 24 h, cooldown
   48 h. R lands → only A gets an episode; B and C stay on `lastGood`
   (fleet target held). A healthy → R not soaked; no wave. `advance(24h)`
   → event soaked, wave starts, B episode; B healthy → C episode; C healthy
   → `lastGood = R`, pins cleared, wave `done`.
2. **Skip-ahead**: R2 and R3 land during the cooldown; A moves to each;
   when the cooldown ends the wave targets R3; after it, R2 is `skipped`,
   `lastGood = R3`.
3. **Cooldown**: a second release soaked 1 h after wave 1 started does not
   start a wave; `advance(47h)` still not; `advance(1h)` yes. `nextWaveAt`
   is in the status meanwhile.
4. **Bad canary**: A fails twice on R, rolls back; R is TIMEOUT → not
   soaked, no wave; after the retry fails, quarantined; next release soaks
   normally.
5. **No canary**: soak 24 h, no `canary:true` host → event once, the wave
   starts immediately for the first release; cooldown still spaces the next.
6. **Canary ahead does not block lastGood**: A healthy on R3 while B and C
   finish R2 → `lastGood = R2`.
7. **Wave aborted**: wave into R, B gating fails twice and rolls back
   (R TIMEOUT); C was pinned but idle → unpinned, wave `aborted`; next wave
   waits for the cooldown from this wave's start.
8. **guard, Flatcar paced**: canary pinned to `current` on the first tick;
   workers unpinned, fleet target `lastGood`. Canary observed boot → gate →
   healthy → `firstHealthyAt`. `advance(soak)` → wave pins every worker at
   once; observed boots gate; all healthy → `lastGood`, pins cleared.
9. **Persistence**: `wave` and `firstHealthyAt` survive `New` on the saved
   file; the cooldown is honoured across the restart.
10. **Status**: `soak`, `cooldown`, `wave`, `nextWaveAt`, `canaries`,
    `firstHealthyAt`, `soaked`, `skipped` appear as specified.

## Rollout of this change

Defaults keep v1.0.0 behaviour. The homelab (ArgoCD, `ghcr.io/jeefy/booty`
pinned by tag, `--autopilot=full`) adds `--autopilotSoak=24h
--autopilotCooldown=48h` and flags one Bluefin host `canary: true` once a
release carrying this ships; adding the flags to v1.0.0 is a start-up
error (unknown flag).
