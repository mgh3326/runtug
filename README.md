# runtug

Launch and supervise coding-agent runs across your hosts.

runtug is a small Go CLI around herdr panes, the quota catalog, handoffkeep
tasks, arbiter leases and panewire routing. It does not own review or landing;
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
policy (free pools, then WASTE subscription pools, the credit pool, then
ranked spend; inside a tier the lowest grade that fits the task, then the pool
with the lower projected use at reset), and admitted in order — each must have fresh host-presence
evidence for its quota account and pass the quota gate. Unknown, stale or
degraded evidence is never guessed admissible: selection fails closed.
Every considered rung and its drop reason is recorded on the placement event;
`--dry-run` prints the record and executes nothing.

A subscription pool is WASTE when its use projected to reset (pacelever's
pace: used ÷ elapsed share of the window) stays below the policy's
`waste_below_projected_pct`; a pool projected at or above
`exclude_at_projected_pct` is never selected. Urgency is resolved and recorded
but does not reorder anything: the free pool (SWE-2) stays first for every
task, and urgent work reaches a paid pool only when the free rung is refused
(a live hold or an exhausted window). Pass `--urgency normal|urgent`; when it
is omitted the bound hk task's priority decides (1–2 is urgent) and otherwise
the default is normal. An explicit flag always wins. The record and the
`--dry-run` output carry `urgency=<v> source=<flag|task-priority|default>`,
the tier order used, the ordering rule and, for the chosen rung, its tier,
grade and projected use at reset.

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
`WRK_COMPLETION_TIMEOUT_S`, then one hour. `--wait` refuses `--dry-run`.

```sh
runtug done task42-worker --report ~/work/herdr-inbox/jobs/task42-worker/report.md
runtug escalate task42-builder --question 'A decision is needed before proceeding'
runtug restore task42-worker
runtug reap --include-builders                   # preview
runtug reap --include-builders --apply           # close verified terminal panes
runtug prune --stale-age 24h                     # preview stale absent-pane jobs
runtug heavy -- go test ./internal/changed -run TestChanged
runtug heavy status
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

Installation is explicit. The project never replaces `wrk` or updates a service
automatically. For source builds, Go 1.27 is required:

```sh
CGO_ENABLED=0 go build -trimpath -o dist/runtug ./cmd/runtug
```

Dependencies are installed separately: herdr, handoffkeep, arbiter, panewire and
`scopefuel` (or a compatible `pacelever` adapter). No database server is needed
by runtug. Artifact upload confirmation additionally uses the operator's Drive
client and `fpbulk`; SSH transfers use rsync.

Existing `~/.config/wrk/hosts.toml` is read in place. See
[every configuration key](docs/config.md), the [design and parity table](DESIGN.md),
and the [versioned safety preamble](internal/runtug/brief-policy.json).

Start migration in shadow: inspect `spawn --dry-run`, compare profiles and hosts,
then use distinct job IDs for isolated runtug runs beside wrk. Never submit the
same live task twice. The repository's mergewake policy has `enabled: false`.

The v0.1 transport supports local and SSH runtug hosts. Legacy `via="hub"`
spawn endpoints still invoke wrk and are refused until the hub supports runtug.
Old rollback aliases require matching catalog rows; there is no embedded model
fallback. Advanced wrk heavy queue fairness/requeue tickets are not implemented;
the interoperable host slot lock, load wait and inherited lock lifetime are.
