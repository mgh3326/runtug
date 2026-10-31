# runtug

Launch and supervise coding-agent runs across your hosts.

runtug is a small Go CLI around herdr panes, the quota catalog, handoffkeep
tasks, a local job ledger (leases, job events) and panewire routing. It does not own review or landing;
[mergewake](https://github.com/mgh3326/mergewake) does.

```sh
runtug spawn -c ~/work/project.task42 \
  -p brief.md -w workers -l task42-worker --t T1 \
  --task 42 --owner project-builder --job task42-worker --host auto \
  --report ~/work/herdr-inbox/jobs/task42-worker/report.md --dry-run
```

Omit `-m` (or pass `-m auto`) and runtug selects the profile mechanically:
the quota catalog's live rungs are filtered by the task's declared grade,
admission gate, role and capability tags, ordered by the versioned pool-order
policy (paid pools at waste risk first, then free pools, then WASTE
subscription pools, the credit pool, then ranked spend; inside a tier the lowest grade that fits the task, then the pool
with the higher waste urgency — the unused projected share per hour until its
longest window's reset (7d, monthly; sub-daily windows such as 5h never rank,
they gate admission only); a pool whose reset can be paced always ranks ahead
of one whose cannot, and an unpaceable pool ranks by its projected use at
reset), and admitted in order — each must have fresh host-presence
evidence for its quota account and pass the quota gate. Unknown, stale or
degraded evidence is never guessed admissible: selection fails closed.
Every considered rung and its drop reason is recorded on the placement event;
`--dry-run` prints the record and executes nothing.

A subscription pool is WASTE when its use projected to reset (pacelever's
pace: used ÷ elapsed share of the window) stays below the policy's
`waste_below_projected_pct`; a pool projected at or above
`exclude_at_projected_pct` is never selected. A paid pool is at **waste
risk** when its longest window's projected unused share at reset is at least
`waste_risk.min_unused_pct` (default 20; an optional
`waste_risk.horizon_hours`, default 0 = no limit, also bounds the time to
reset): it ranks ahead of the free seats by waste urgency, and its sub-daily
windows (5h) refuse it only when exhausted (100% used), never on pace.
Otherwise the free pool (SWE-2) stays first. Urgency is resolved and
recorded but does not reorder anything: urgent work reaches a paid pool
that is not at waste risk only when the free rung is refused (a live hold
or an exhausted window). Pass `--urgency normal|urgent`; when it
is omitted the bound hk task's priority decides (1–2 is urgent) and otherwise
the default is normal. An explicit flag always wins. The record and the
`--dry-run` output carry `urgency=<v> source=<flag|task-priority|default>`,
the tier order used, the ordering rule and, for the chosen rung, its tier,
grade and projected use at reset. Every candidate carries `waste_risk` and,
when its longest window can be paced, `unused_at_reset_pct`; a dropped
waste-risk pool whose short window is used up says `window_exhausted`.
Every ranked candidate carries
`waste_urgency` and `reset_hours` when its pool's longest window can be
paced, and `waste_order` states whether it ranked by that urgency (`reset`)
or by the projected-at-reset fallback. Free-tier seats are excluded only by
an observed free limit (the rung's gate refusal), never by their shared
pool's used or projected share.

`runtug simulate --selection FILE [--now RFC3339] [--policy FILE]
[--policy-set key=value] [--set
pool:used=N,reset=RFC3339|+Nh[,window=D][,projected=N]]` replays the
ranking on a recorded selection input — a minimal JSON document of pool
usage, reset times and catalog candidates, or a `job.placement` event's
`payload.selection` — and prints the decision record with the ranked choice.
It reads no quota, calls no gate and spawns nothing; `--policy` and
`--policy-set waste_risk.min_unused_pct=30` try other policy thresholds for
the run. A malformed pool `resets_at` or `window` exits 70. The input shape
is documented in `docs/config.md`.

```sh
runtug plan --grade A --t T1 [--role worker] [--tags unity] [--effort high] [--json]
```

`runtug plan` is the live, read-only question "which host and which model
would a spawn get right now": it runs the same selection and placement code
as `spawn` — including each candidate host's quota gate — and prints the
chosen host and model, the runners-up and every drop reason, without a task,
brief, worktree, spawn, hk write or job record. Exit codes are in
`docs/config.md`.

```sh
runtug spawn -c ~/work/project.task42 -m cmd-dsflash41 \
  -p brief.md -w workers -l task42-worker --t T1 \
  --task 42 --owner project-builder --job task42-worker --host local \
  --override-reason "pin dsflash for parity test" --report report.md
```

Explicit `-m` or `--host` stays a forced override but requires a recorded
`--override-reason`. The quota gate still applies to overrides. Escalation
and consult-only catalog gates are excluded unless `--allow-escalation` is
given. Remove `--dry-run` to launch. The preview includes the standard
host-safety preamble. Task titles must carry a `(grade B, T1)` style marker;
the catalog's grade for the selected effort must cover that grade. Testers
also need `--parent-job BUILDER_JOB` and an independent model family —
automatic selection keeps the builder's family excluded.

```sh
runtug spawn -c ~/work/project.task42 -m builder-sol-max -p brief.md \
  -w workers -l task42-builder --t T2 --task 42 --role builder \
  --lane project-builder --parent operator-desk --host auto \
  --override-reason "builder pin per hk 42" --wait --wait-timeout 45m
```

`--wait` blocks after a successful spawn until the job's completion, READY,
escalation or lost/revoked signal arrives on the parent or owner lane, then
prints a one-line summary and exits: 0 done/ready/joined, 83 escalate,
84 lost/revoked, 85 timeout. The watcher reads durable job events and the
lane inbox with bounded backoff — a plain blocking CLI, no resident process.
`--wait-timeout` requires `--wait`; without it the bound falls back to
`RUNTUG_COMPLETION_TIMEOUT_S`, then one hour. `--wait` refuses `--dry-run`.

Every spawn also claims a job record in handoffkeep (`/v1/jobs`) on the
executing host before the local claim, then merges allowlisted metadata as
the job moves `claimed → spawned → completed | escalated | lost`. `done
--result findings.json` writes a JSON findings object (≤64 KiB) together with
`completed` in one PUT, before the relay emit. `--parent-job` resolves the
local job directory first and falls back to the hk record on any host, and a
delegated `--wait` watches the record alongside local signals. See
[docs/config.md](docs/config.md) for fields and failure behavior.

```sh
runtug done task42-worker --report ~/work/herdr-inbox/jobs/task42-worker/report.md [--result findings.json]
runtug escalate task42-builder --question 'A decision is needed before proceeding'
runtug restore task42-worker
runtug reap --include-builders                   # preview
runtug reap --include-builders --apply           # close verified terminal panes
runtug prune --stale-age 24h                     # preview stale absent-pane jobs
runtug gc                                        # expire leases, clean ledger records of agents gone from herdr
runtug hosts
runtug profiles
runtug find task42-worker
runtug name-sync --apply
runtug lanes-missing
runtug artifacts move ./dataset --dest ~/Drive/host-a/dataset
runtug artifacts sweep --job task42-worker        # preview
```

`artifacts move` and `sweep` require `--apply` for writes, eviction or deletion.
Move retains source originals. Sweep requires a closed task, ended job, absent
pane, policy classification, archive confirmation and a matching file hash.
Unclassified files, records, credentials and symlinks are retained.

Download a release binary for Linux/macOS, amd64/arm64, or use the checksum
verifying installer:

```sh
curl -fsSLo /tmp/runtug-install.sh https://raw.githubusercontent.com/mgh3326/runtug/main/install.sh
sh /tmp/runtug-install.sh --tag v0.1.0 --bin-dir ~/.local/bin
```

Installation is explicit. The project never replaces another tool or updates a service
automatically. For source builds, Go 1.27 is required:

```sh
CGO_ENABLED=0 go build -trimpath -o dist/runtug ./cmd/runtug
```

Dependencies are installed separately: herdr, handoffkeep, panewire and
`pacelever` (or a compatible quota CLI). No database server is needed
by runtug; the local job ledger is an embedded SQLite file (pure Go, no cgo)
and runtug no longer executes the agent-skills `arbiter` script (see
[docs/config.md](docs/config.md#local-job-ledger)). Artifact upload confirmation additionally uses the operator's Drive
client and `fpbulk`; SSH transfers use rsync.

The hosts config lives at `~/.config/fleet/hosts.toml`; a config left only at the
retired `~/.config/wrk/hosts.toml` still loads for one release with a warning. See
[every configuration key](docs/config.md), the [design and parity table](DESIGN.md),
and the [versioned safety preamble](internal/runtug/brief-policy.json).

Inspect `spawn --dry-run`, compare profiles and hosts,
then use distinct job IDs for isolated runtug runs. Never submit the
same live task twice. The repository's mergewake policy has `enabled: false`.

The v0.1 transport supports local and SSH runtug hosts. Legacy `via="hub"`
spawn endpoints are refused until the hub supports runtug.
Old rollback aliases require matching catalog rows; there is no embedded model
fallback.
