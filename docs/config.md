# Configuration

Runtug reads `~/.config/fleet/hosts.toml`. `FLEET_HOSTS_CONFIG` overrides the file;
otherwise `XDG_CONFIG_HOME/fleet/hosts.toml` is used when XDG_CONFIG_HOME is set.
For one release a config found only at the retired `~/.config/wrk/hosts.toml`
path (or through `WRK_HOSTS_CONFIG`) still loads with a one-line warning naming
the new location. The same applies to retired environment names: `WRK_LANE_STRICT`,
`WRK_COMPLETION_TIMEOUT_S`, `WRK_MOCK_MCP_CONFIG` and `SCOPEFUEL_E6_ARM` fall back
to their `RUNTUG_*` replacements with a warning; a set `SCOPEFUEL_BIN` is ignored
with a warning. The workspace trust lock stays `.wrk.lock` until old spawner
binaries are drained from every host.
A missing file means a local host with standard harnesses. Malformed TOML,
duplicate keys, root assignments, BOMs, invalid safety booleans and contradictory
hub quota settings refuse with exit 70. Existing tables keep their key names.
Credentials belong in the referenced private credential files, never this repo.

```toml
[local]
machine_id = "host-a"
spawn = true
max_load_ratio = 1.5
max_active = 4
shell_ready_timeout_ms = 120000
pi_anthropic = false
cmd_dsflash41 = false
clinepass = false

[hub]
hub_url = "https://hub.example.invalid"
hub_token_env = "~/.config/example/operator.env"
operator_token_env = "~/.config/example/operator.env"
lanes_token_env = "~/.config/example/lanes.env"
hub_cf_env = "~/.config/example/access.env"
machine_id = "host-a"
session_machine_ids = { default = "host-a", workers = "host-a-workers" }

[hosts.host-b]
ssh = "host-b"
via = "ssh"
herdr_session = "workers"
workspace = "workers"
capacity = 4
tags = ["linux", "gpu", "unity"]
presence_id = "host-b"
cwd_map = { "/home/example/work/project" = "/srv/work/project" }

[placement]
load_threshold = 1.0
wake_after_s = 600

[quota]
url = "https://hk.example.invalid"
credential_env = "HANDOFFKEEP_TOKEN"
presence_ttl_s = 86400
fresh_ttl_s = 300
accounts = { pool-a = "0123456789abcdef" }

[artifacts]
drive_folder = "~/Drive"
fpbulk = "~/.local/bin/fpbulk"
exclude = [".env*", "*.pem", "*.key", "*secret*", "*credential*", ".git/**"]
drivefs_log_dir = "~/Library/Application Support/Google/DriveFS/Logs"
scratch_dir = "~/.local/share/fleet-upload/scratch"
local_delete_at = "drive_confirmed"
```

## `[local]`

| Key | Default | Meaning |
| --- | --- | --- |
| `machine_id` | hub identity, then `local` | This execution host's identity in receipts and policy selectors. |
| `spawn` | `true` | Literal boolean; `false` refuses local admission with 80, including explicit local requests. |
| `max_load_ratio` | unset | Nonnegative load5 / CPU threshold for fallback spillover when hub placement is unavailable. |
| `max_active` | unset | Nonnegative active-agent threshold for fallback spillover. |
| `heavy_max` | legacy | Accepted and ignored; the `heavy` command was removed. |
| `heavy_load_wait` | legacy | Accepted and ignored; the `heavy` command was removed. |
| `shell_ready_timeout_ms` | `120000` | Startup readiness window, integer 30000–300000. |
| `pi_anthropic` | `false` | Literal capability opt-in required by pi policy rows. |
| `cmd_dsflash41` | `false` | Literal capability opt-in required by cmd policy rows. The provisioned cmd account must have no persisted MCP servers. |
| `clinepass` | `false` | Literal capability opt-in required by the `cc-*` seats, which route the Claude Code harness through the host's local ClinePass proxy (`CLINEPASS_GATE_URL`, key read at spawn time from `CLINEPASS_GATE_KEY_FILE`; defaults are a loopback URL and `~/.config/cliproxyapi-key.txt`). |
| `tags` | unset | Capability tags offered by this host for job placement (`spawn --tags`). Lowercase tag names. |
| `presence_id` | `machine_id` | Identity this host uses in pacelever host-presence records. |
| `brief_policy` | embedded policy | Path to the versioned brief-policy JSON; leading `~/` expands. |
| `brief_preamble` | selected policy block | Explicit named preamble block for this host. |
| `pool_policy` | embedded pool-order policy | Path to the versioned pool-order JSON used by automatic model selection. |
| `fleet_usage_alert_gb` | legacy/read-only | Retained config key; standalone disk alerts are not emitted in v0.1. |

Additional boolean capabilities can be named by custom seat policy rows. A
missing or nonboolean capability fails closed. `standard` in seat policy means
no additional host capability beyond local admission and proven harness startup.

## `[hub]`

| Key | Meaning |
| --- | --- |
| `hub_url` | Panewire hub URL. |
| `hub_token_env` | Operator credential-file path for placement/quota. Configuring it opts into the hub quota gate and requires hub_url. |
| `operator_token_env` | Operator credential file for control operations and builder lane fallback. |
| `lanes_token_env` | Dedicated machine-bound lane credential; preferred for lane registration/removal. |
| `hub_cf_env` | Optional access-service credential file passed to panewire. |
| `machine_id` | Host identity fallback. |
| `session_machine_ids` | Herdr session → machine identity table used for lane placement. |
| `quota_gate` | Only `"local"` is accepted: explicit local-only quota mode. It contradicts hub_token_env; remove one of those keys. |

Worker lane credentials use lanes_token_env, then hub_token_env. Builder lane
credentials use lanes_token_env, then operator_token_env, then hub_token_env.
Panewire owns authentication and relay transport; runtug never prints credentials.

## `[hosts.NAME]`

| Key | Default | Meaning |
| --- | --- | --- |
| `ssh` | `NAME` | SSH config target; BatchMode is used. |
| `via` | `ssh` | `ssh` (default; unchanged when unset) or `hub`: spawn, worktree preparation and quota refresh go through the panewire hub (see [Hub transport](#hub-transport-via--hub)). A `hub` host never falls back to ssh. |
| `herdr_session` | `workers` | Remote HERDR_SESSION. |
| `workspace` | spawn `-w` | Remote workspace. |
| `cwd_map` | explicit override | Inline table of local prefix → remote prefix; longest prefix wins. Only needed as an exception: the default remote cwd is `$HOME/work/<repo>` on the target, cloned when missing. For a job run from a linked git worktree the default is `<work root>/<worktree dir>`, where the work root is derived from entries that map a checkout directly under the local work root (else `$HOME/work`). |
| `cwd_keys` | legacy | Kept for legacy hub configurations; not used. A hub host's cwd key is the canonical repo name. |
| `hub_wait` | `120` | `via = "hub"` only: seconds (1..300) the hub waits for the node to answer a spawn. A first spawn that clones a large repo may need more. |
| `brief_doc_prefix` | `briefs/` | `via = "hub"` only: handoffkeep key prefix the brief is uploaded under; it must match the node's own `brief_doc_prefix` (ends in `/`). |
| `tags` | unset | Capability tags offered by this host. A job's required `--tags` must be a subset for the host to be a placement candidate. |
| `presence_id` | host `NAME` | Identity this host uses in pacelever host-presence records. |
| `capacity` | unset | Inventory value displayed by hosts. Authoritative capacity comes from panewire placement and remote admission. |
| `heavy` | legacy | Accepted and ignored; the `heavy` command and its host lock were removed. |
| `wake` | off | `"panewire"` marks the host wakeable: the placement policy may request `panewire node wake` when asleep, then waits for the node to connect. |
| `wake_hold` | `10` | Burst hold in minutes. |
| `wake_wait` | `90` | Burst command timeout in seconds; wake refusal returns 81. |
| `brief_policy`, `brief_preamble` | host policy default | Optional initiator-side preview settings for this host; deploy matching policy on the execution host. |

When the job cwd is a linked git worktree and placement picks a remote host,
runtug prepares the same worktree there before spawning: it ensures the repo
clone (from the local `origin` URL) under the host's work root, runs
`git fetch origin`, and runs `git worktree add` for the local branch, tracking
`origin/<branch>` when it exists (a PR head) and otherwise creating it from
origin's default branch. An existing remote worktree is reused when it is on
that branch; a different branch, a detached local HEAD, local commits missing
on the target or any failing step refuses the spawn with exit 81 — it never
tries another host or falls back to local. An explicit `cwd_map` entry decides
the worktree path (its clone is the canonical repo name beside it); an
unreachable host is skipped as before. `--dry-run` prepares nothing.

### Hub transport (`via = "hub"`)

A host with `via = "hub"` is spawned through the panewire hub (`POST /v1/spawn`,
`spawn_v2`) instead of ssh. It needs `[hub] hub_url` and an operator credential
file (`operator_token_env`, else `hub_token_env`; `hub_cf_env` if the hub sits
behind access); the files must be private and are never echoed. For one spawn
runtug:

1. uploads the brief as an hk document `<brief_doc_prefix><job>.md`
   (`handoffkeep doc put --kind brief --job <job>`) and sends the key as
   `brief.doc`; the hub never sees the text. A brief over 64 KiB is refused
   (exit 2); a retry overwrites the same key.
2. for a job started from a linked worktree, sends a `checkout` spec that the
   node's `worktree.prepare` turns into the same worktree: `mode: branch` for
   a branch whose local HEAD is already contained in `origin/<branch>` (the
   node tracks origin's tip), otherwise `mode: detached` at the exact `sha`
   with `fetch: pull/<n>/head` when a local `origin/pull/<n>/head` ref points
   at HEAD, or `fetch: refs/heads/<branch>` of an origin branch containing it
   (a detached HEAD, or a branch whose commits sit on another origin ref).
   Only refs already known locally are read; nothing is fetched. Commits on no
   origin ref, an origin URL without an https form (`git@host:o/r` and
   `ssh://` become `https://host/o/r`), or a subdirectory start refuse with
   exit 81 before anything is uploaded. The worktree directory name is the local
   one. A job from a canonical checkout sends no `checkout` and runs in the
   node's `cwd_map[<repo name>]`.
3. sends the flags the hub's allowlist carries (`-m -l --t --job --owner
   --effort --role --lane --parent --task|--parent-job --report-relative -L --urgency
   --builder-family --builder-job`). The node supplies workspace, host and the
   override reason. A flag the allowlist cannot carry (`--keep`, `--job-dup-ok`,
   `--task-hk-bypass`, `--allow-escalation`, `--operator-request`,
   `--requested-by`, `--purpose`, `--lane-defer`) refuses the spawn (exit 2)
   instead of being dropped. A report is sent as `--report-relative <path>`,
   relative to the jobs root, and the node resolves it under its own jobs root,
   so the two hosts' home directories may differ. This applies to `--report`
   under the caller's jobs root and to a caller-given `--report-relative`
   alike; no absolute `--report` crosses the hub. A report outside the
   jobs root is refused (exit 2), as is an absolute or `..` `--report-relative`
   or a path with characters the hub does not accept. A hub without
   `--report-relative` answers 400 `invalid_request`; runtug then stops with a
   message naming the missing capability instead of retrying with an absolute
   path. Upgrade panewire on the hub to a version whose spawn allowlist carries the
   flag.
   `--builder-family` may be sent without `--builder-job` for `--role reviewer`
   only (the author's job is not bound to the task): the reviewer then runs as
   a top-level job, still excludes that family, and neither claims nor links
   the task, so several passes on one task all spawn. Any other role keeps the
   "go together" refusal (exit 2).
4. the node's `spawn.json` must map the repo name as a `cwd_key` and set
   `work_root`; `spawn_disabled`, `cwd_unmapped`, `runtug_missing` and
   `brief_doc_unavailable` refuse that host (recorded as `hub_<reason>`) and
   `--host auto` moves to the next candidate.

The target account is refreshed with the `quota.refresh` node command instead
of `ssh <host> pacelever refresh`; `measure_timeout_s` still bounds it.

Failures never fall back to ssh (design D4):

| Hub answer | Result |
| --- | --- |
| Hub unreachable, timed out, 5xx | Host refused, reason `hub_unavailable`. `--host auto` continues with the other candidates; with none left the usual refusal applies (80 when local spawning is off, else the job runs locally). `--host NAME` exits 81 `hub unavailable`. A quota refresh meets the same outage the same way. |
| 503 `spawn_capability_required` | Node predates `spawn_v2`: host refused, reason `hub_spawn_capability_required`. |
| 503 `node_unavailable` | Node asleep or not accepting: treated like an unreachable host, so a wakeable host is woken (and waited for through the hub connection, never ssh). |
| 401 / 403 / 400 | Host refused, reasons `hub_auth` / `hub_invalid_request` (exit 70 semantics). |
| `checkout_refused:<outcome>` (rc 81) | Exit 81 naming the outcome (`branch-elsewhere`, `sha-missing`, `origin-mismatch`, `dirty`, `locked`, `git-failed`, `git_too_old`); no other host is tried. `busy` is retried once. |
| 504 pending, `lost`, node `timeout` | Exit 75: the node may still run the job, so nothing is spawned elsewhere. |
| Node spawn rc 0 | The `OK pane=… job=…` receipt is printed with `host=NAME`. Other rc values follow the ssh rules (2, 70, 74, 75, 76, 79 and 82 stop the round; others refuse the host). |

`--dry-run` uploads and sends nothing.

`--host auto` runs the local placement policy: required `--tags` must be a
subset of a host's tags (a job with no tags may go anywhere), hosts with fresh
presence evidence for the needed quota account rank before unknown logins
(degraded reads count as unknown, never absent), and awake candidates order by
load5/ncpu with any host at or above `load_threshold` tried only when nothing
else qualifies. The spawn-time quota gate on the chosen host remains the final
admission check; a refusal moves to the next candidate. Asleep `wake` hosts
are only awakened under the documented rules — a capability no awake host
has, every awake candidate over threshold after `wake_after_s`, the needed
account measured only on the sleeper, or an operator request — then runtug
waits for the node and places the job. Explicit `--host NAME` stays a forced
override; an unreachable wakeable target is woken once. Every decision is
recorded as a `job.placement` event in the job record.

`--exclude-host a,b` removes the named hosts from `--host auto` placement: an
excluded host — the local machine included, by its machine id — is never
ordered, gated or woken, and its candidate records `excluded_host`. When no
candidate remains the spawn fails with the usual placement refusal. An explicit
`--host NAME` (or `--host local`) that names an excluded host is refused with
exit 2. The list is initiator-local and is not sent to a delegated host.

Before trying a remote candidate (including an explicit `--host NAME`), runtug
resolves that host's most recent fresh presence for the launch pool, then asks
`pacelever gate --account-key POOL:FINGERPRINT --json` to judge its shared
observation. The spawner's `[quota.accounts]` does not pin remote accounts.
Missing, ambiguous, stale or degraded target evidence refuses that host; auto
placement tries the next candidate. Local placement keeps the local gate path.
The selection's `quota_gates` and placement candidate's `quota_gate` record the
host, account key, evidence source, outcome and exit code of each attempt.

When the target's shared observation is missing or stale and the spawning host
does not hold that account (no `[quota.accounts]` pin or local presence record
for the key), pacelever cannot refresh it on demand. runtug then runs
`pacelever refresh POOL` on the target over SSH, bounded by `measure_timeout_s`,
re-reads the shared observation and judges it (for a `via = "hub"` host the
refresh is the hub's `quota.refresh` node command, not ssh). A failed, timed-out or
unpublished measurement keeps the refusal; `quota_gates` records
`measured` (`ok` or `failed`) and a `measure_*` reason. A gate that reports its
`gate_basis` is also recorded as `basis`.

The spawning host needs a pacelever version that supports account-bound gate
receipts. A successful response must confirm the requested `account_key` and
pool; versions that silently ignore the flag are refused. The target still
runs its own final local gate before spawning. Upgrade pacelever on spawning
hosts before rolling out this change; roll back runtug to restore the earlier
placement path if needed. No remote checkout provisioning changes are included.

Runtug must already be installed on SSH targets. `_spawn-stdin` transports a
structured request and original brief through stdin, uses a private temporary
brief file and deletes it after spawn. The target composes its own preamble.
No host installation or service management is performed by spawn.

## `[placement]`

| Key | Default | Meaning |
| --- | --- | --- |
| `load_threshold` | `1.0` | load5/ncpu value above which a host takes no new job while anything else qualifies. |
| `wake_after_s` | `600` | How long the task must have waited before saturated awake hosts justify waking a sleeper. |

## `[quota]`

Shared-quota read settings for presence-aware placement. URL and credential
follow the pacelever convention: `HANDOFFKEEP_URL`/`HANDOFFKEEP_TOKEN` in the
environment or `~/.config/handoffkeep/config.env`; the keys below are explicit
overrides.

| Key | Default | Meaning |
| --- | --- | --- |
| `url` | `HANDOFFKEEP_URL` | Shared quota cache endpoint; HTTPS required outside loopback. |
| `credential_env` | `HANDOFFKEEP_TOKEN` | Name of the environment variable holding the bearer token. |
| `presence_ttl_s` | `86400` | How recent a host-presence record must be to count as login evidence. |
| `fresh_ttl_s` | `300` | How recent a shared observation must be to count as fresh. |
| `measure_timeout_s` | `60` | Bound on the on-target `pacelever refresh` run for a remote account the spawning host does not hold. |
| `accounts` | unset | Optional pool → 16-hex account-fingerprint table for exact account keys. |
| `node_env` | `~/.config/panewire/hub-node.env` | Panewire node env file; `HUB_MACHINE_ID` in it is the host id written to job records. `RUNTUG_NODE_ENV` overrides the path. |

## Job records in handoffkeep

Each spawn writes a record to hk `/v1/jobs` on the executing host (the design
is `design/2026-10-30/cross-host-jobs`): the claim `POST {job_id, task_id,
host}` runs before the local claim and a taken id refuses with exit 74, like
the ledger's duplicate. `host` is the node's `HUB_MACHINE_ID`. hk unreachable
or unconfigured fails the spawn closed, as the task claim does.

After the brief lands, runtug merges `{state:"spawned", role, label, family,
profile, model, pool, owner_lane, parent_lane, parent_job, pane_id,
worktree:{origin, branch|null, head}}` — the allowlisted fields only; brief
text, command lines, pane output and absolute local paths are never written.
`runtug done` writes `{state:"completed", report_doc}` plus `result` when
`--result FILE` (a JSON object of at most 64 KiB) is given, in the same PUT,
and emits the relay only after the PUT returns; `escalate` writes
`{state:"escalated"}` and the watcher writes `{state:"lost"}` after two
missing-pane observations. A failed write after the pane exists degrades to a
`job.record_failed` event and a warning; the relay still goes out so readers
fall back.

`--parent-job` resolves the local job directory first, then `GET
/v1/jobs/<job>` on any host — waiting up to 30 s for a lagging record before
refusing with exit 75. `spawn --wait` on a delegated job also watches the hk
record: `completed` exits 0 (the result is read from the record by the
caller), `escalated` exits 83, `lost` exits 84, and a record still pending,
absent or unreadable leaves the local signals and the usual deadline to
decide (the unreadable case warns once with reason `hk-unavailable`).

Automatic model selection (`spawn` with `-m` omitted or `-m auto`) reads the
live bench catalog and pool status from the quota CLI, then orders candidates
under the versioned pool-order policy (`[local].pool_policy` or
`RUNTUG_POOL_POLICY`, else the embedded copy): paid pools at waste risk
first (see below), then free pool/model sets, then WASTE subscription pools,
then named credit pools, then ranked spend pools. Inside a tier the lowest catalog grade that satisfies the task wins,
then the pool with the higher waste urgency — the unused projected share
(100 − projected) per hour until its longest account window's reset (7d,
monthly; sub-daily windows such as a 5h rate window never rank a pool, they
stay admission gates only). A pool whose longest window's reset can be paced
always ranks ahead of one whose cannot; unpaceable pools keep the lower
projected use at reset (the used percentage when it cannot be paced yet) —
then the smaller effort, then the catalog score.

The projection is pacelever's pace: for each account-scope bucket in the
quota status, used percentage ÷ elapsed share of its window, paced at the
status document's `generated_at`; a bucket past its reset or under 5% into its
window has none, and a pool's projection is its highest bucket's. Two policy
keys use it, both defaulting to 100:

| Key | Default | Meaning |
| --- | --- | --- |
| `waste_below_projected_pct` | `100` | A non-credit subscription pool projected below this at reset is WASTE: the rest is lost at reset, so it is spent before credit and ranked pools. Without a projection the quota status `verdict.waste` flag decides. An explicit `0` disables projected waste. |
| `exclude_at_projected_pct` | `100` | A pool projected at or above this is dropped as `projected_exhaust` (a waste-risk pool: projected over its windows of a day or more only, see below). A free-tier rung is kept for admission instead and survives only when the gate's `gate_basis` is a free bucket (`free-bucket:…`); any other basis drops it as `projected_exhaust`. Must be above 0 and not below `waste_below_projected_pct`. An over-consumed reading (used above 100%) still projects, so it is excluded. |

A paid pool is at **waste risk** when the projected unused share of its
longest window (a day or more) at that window's reset — waste urgency ×
hours to reset, i.e. 100 − its projection — is at least
`waste_risk.min_unused_pct`, however far the reset is. Free seats and
`credit_pools` are never at waste risk, and neither is a pool whose
longest window cannot be paced (for example the first 5% of a fresh
window). Waste-risk pools rank ahead of every tier, free included, and
among themselves by waste urgency first (a pool resetting sooner with the
same unused share comes first), then grade fit, effort and score; every
other pool keeps the tier order above. For a waste-risk pool sub-daily
windows (e.g. 5h) are used up rather than paced: they never contribute to
its `projected_exhaust` check and refuse it only when exhausted — used at
or above 100% (dropped as `window_exhausted`) or the quota gate's own
exhausted refusal. A pool not at waste risk keeps the pace-based
projection over all its windows. The quota gate itself never refuses on
pace (pacelever: "pace ranks and warns; it never denies"), so no gate flag
is passed.

| Key | Default | Meaning |
| --- | --- | --- |
| `waste_risk.min_unused_pct` | `20` | Minimum projected unused share (percent) of the longest window at its reset for a paid pool to be at waste risk. `0`–`100`. |
| `waste_risk.horizon_hours` | `0` | Optional: when above 0 the longest window must also reset within this many hours. `0` means no horizon limit. |

Both keys live in the pool-order policy under `"waste_risk": {...}`; absent
keys take the defaults, and `runtug simulate --policy`/`--policy-set` try
other values before the policy is changed.

Non-free candidates additionally need a
readable `ok` nonstale spend status; every candidate needs fresh
host-presence evidence for its quota account on a spawn-eligible host and a
passing quota gate. Any unknown,
stale or degraded evidence drops the candidate — selection never guesses and
fails closed. Every catalog row's outcome (grade, gate, policy tier,
presence, quota or seat reason) is recorded on the `job.placement` event;
`--dry-run` prints the full record and emits nothing. Explicit `-m`/`--host`
overrides require `--override-reason`, which is recorded; escalation and
consult-only gates stay excluded unless `--allow-escalation` is given, and
quota admission still applies to overrides.

Selection records urgency. `spawn --urgency normal|urgent` sets it; when the
flag is omitted the bound hk task's `priority` is read from the existing task
precheck (priority 1 or 2 resolves to urgent, anything else or unreadable to
normal), and an explicit flag always wins. Urgency does not change the
order: the waste-risk override, the policy `order` and the grade-fit rule
apply to every task, and urgent work reaches a paid pool that is not at
waste risk when the free rung is refused or excluded. The `job.placement` selection
record carries `urgency`, `urgency_source` (`flag`, `task-priority` or
`default`), the `tiers` order used, a one-line `decision`, the ordering
`rule` with both thresholds and the waste-risk rule, `waste_risk` (the
`horizon_hours`/`min_unused_pct` in force), `projected_pct` and `used_pct`
per candidate, `waste_risk` (`true`/`false`) on every candidate and
`unused_at_reset_pct` (its input) whenever the longest window could be
paced,
`waste_urgency`, `reset_hours` and `waste_order` (`reset` or
`projected_fallback`) per ranked candidate,
and on `chosen` its `tier`, `grade`, `waste_risk`, `projected_pct`,
`waste_urgency` and a one-line `why`;
`--dry-run` prints `urgency=<v> source=<s> tiers=<a>><b>...` before the full
record.

### `runtug simulate`

`runtug simulate --selection FILE [--now RFC3339] [--policy FILE]
[--policy-set key=value] ... [--set
pool:used=N,reset=RFC3339|+Nh[,window=D][,projected=N]] ...` replays the
selection ranking on a recorded input and prints the decision record
(`mode: "simulate"`). It is hermetic: no quota reads, no gate calls, no
spawn, no job writes. The input file is a minimal JSON document — or a
`job.placement` event whose `payload.selection` carries the same fields:

```json
{
  "now": "2026-10-31T07:24:00Z",
  "grade": "A",
  "role": "worker",
  "effort": "",
  "allow_escalation": false,
  "credit_pools": ["anthropic-api"],
  "pools": [
    {"id": "claude", "status": "ok", "class": "spend", "waste": true,
     "used_pct": 31, "projected_pct": 31.8,
     "resets_at": "2026-10-31T16:48:00Z", "window": "7d"}
  ],
  "candidates": [
    {"profile": "sonnet", "seat": "sonnet", "effort": "high", "grade": "A",
     "pool": "claude", "gate": "default", "model_id": "m",
     "free": false, "free_limit": false, "score": 90}
  ]
}
```

`--now` wins over the document's `now`, which wins over the clock. A pool's
projection is `projected_pct` when given, else pacelever's pace over
`used_pct`/`window`/`resets_at` (or `reset_hours` relative to now); waste
urgency is (100 − projected) ÷ hours-to-reset of the longest window of a
day or more (sub-daily windows never rank), and pools without a readable
reset rank by projected order. `free` marks a free-tier rung; `free_limit`
records an observed free limit — the only exclusion a free rung honors, so a
shared pool's `used_pct` or projection never drops it. `--set` (repeatable)
overrides or creates a pool and repaces it: `used` and `projected` are
percentages, `reset` an RFC3339 instant or `+Nh`/`+Nd` relative to now, and
`window` a quota duration (the entry's own window, else 7d). Candidates are
the catalog row fields plus `seat`; a recorded `job.placement` selection's
`candidates` decode directly, and when the input is the recorded
`payload.selection` record itself the request comes from its `requested`
(`at` is now), fields beyond the candidate schema are ignored, and the pool
table is rebuilt from the candidate rows' `used_pct`/`projected_pct` — with
no recorded reset those pools rank by the projected fallback until `--set
pool:reset=…` supplies one.

A pool's `resets_at` must be an RFC3339 instant or `+Nh`/`+Nd`, and a
non-empty `window` a quota duration; anything else fails with exit 70
instead of silently ranking by the projected fallback. A sub-daily `window`
is a short window (it gates a waste-risk pool only when `used_pct` reaches
100); any other window is the pool's longest.

`--policy FILE` loads the pool-order policy for this run instead of
`[local].pool_policy`/`RUNTUG_POOL_POLICY`. `--policy-set key=value`
(repeatable, applied after `--policy`) overrides one threshold:
`waste_risk.horizon_hours`, `waste_risk.min_unused_pct`,
`waste_below_projected_pct` or `exclude_at_projected_pct`. A malformed
term or unknown key exits 2; a value the policy rejects exits 70. The record
carries `policy_file` and `policy_overrides`.

### `runtug plan`

`runtug plan --grade G --t T0..T3 [--role R] [--tags a,b] [--effort E]
[--urgency normal|urgent] [--exclude-host a,b] [--builder-family F]
[--routing-role R] [--allow-escalation] [--json]` answers "right now, which host and which
model would a spawn get" without a task or brief. It is live and read-only:
it runs the spawn's selection core (catalog, pool status, presence and each
host's quota gate) with the grade from `--grade` instead of a task title,
then walks the same placement plan for the chosen seat — the local launch
resolution, seat check and gate for this host, the shared-account quota
gate for each remote host — and stops at the first host that admits it. It
never spawns, wakes a host, claims a local job, writes an hk task or job
record or emits a job event; a stale remote account is measured on its
host (`pacelever refresh`) exactly as a spawn would. Remote-only spawn
checks (repository mapping, the remote seat check) are not run.

The text output is `plan …` (request), `chosen host=… seat=…@… model=…
pool=… grade=… tier=… waste_risk=…`, one `runner-up …` line per other ranked
rung in rank order, one `dropped …@… pool=… reason=…` line per filtered
rung, one `host NAME outcome=…` line per placement candidate (`would_spawn`,
`quota_refused`, `missing_tags:…`, `not_tried`, …), `would_wake [...]` when
only a host a spawn would wake remains, and `result refused exit=N: …` on
failure. `--json` prints `{"mode": "plan", "selection": {...},
"placement": {...}, "chosen": {"host", "seat", "model_id", "effort",
"pool", "tier", "grade", "waste_risk"}}` (plus `error`), where `selection`
is the spawn selection record with `mode: "plan"` and `placement` the
placement record.

| Exit | Meaning |
| --- | --- |
| 0 | A host and model were found. |
| 2 | Usage: missing or invalid `--grade`/`--t`/`--role`/`--urgency`/tags, or `--builder-family` without tester/reviewer (or the reverse). |
| 70 | Configuration, unreadable catalog or policy. |
| 77 | No catalog rung was admitted. |
| 80 | Local spawning is disabled and no remote host admitted the seat. |
| 81 | No host admitted the seat; hosts a spawn would wake are listed. |
| 86 / 87 | Every profile the routing role allows is exhausted (`wait` / `escalate`); see "Role routing". |

A local launch or gate refusal keeps its own code, as in `spawn`. Plan
applies role routing like `spawn`; its text output adds a `routing role=…
version=… policy_source=…` line.

## Role routing

The hk document `policy/model-routing` (owned by the operator desk) decides
which catalog profiles a role may run on. runtug constrains the candidate set
with it **before** the pool order (`policy/quota-stop`) ranks anything; the
grade, gate, seat and family rules still apply on top. This applies to
automatic selection in `spawn` (no `-m`, or `-m auto`), `plan` and
`resolve`. An explicit `-m PROFILE` is an operator override and is not
routed; `runtug delegate` (including `--panel`) keeps its own spare-quota
selection and is not routed either.

### The table

runtug reads the first fenced `json` block of the document:

```json
{
  "version": 1,
  "roles": {
    "lead": {"allowed": ["opus", "fable", "codex-sol", "codex-astra"], "effort": "high", "on_exhaustion": "wait"},
    "explorer": {"allowed": ["sonnet", "haiku"], "effort": "medium", "on_exhaustion": "downgrade_to", "downgrade_to": ["haiku"]},
    "reviewer": {"allowed": ["*"], "effort": "high", "on_exhaustion": "escalate", "family_rule": "not_author"}
  },
  "runtug_role_map": {"builder": "lead", "worker": "implementer", "tester": "verifier", "reviewer": "reviewer", "counsel": "lead"}
}
```

| Field | Meaning |
| --- | --- |
| `version` | Must be `1`; any other version is unreadable. |
| `roles.R.allowed` | Catalog profiles (pacelever launch profiles, e.g. `opus`, `codex-sol`, `devin-swe2`) role `R` may run on; `"*"` admits any profile. Matched against the catalog row's `profile`, not the seat alias. |
| `roles.R.effort` | The role's starting effort; `resolve` prints it as the answer's effort. Spawn rung choice is unchanged. |
| `roles.R.on_exhaustion` | `wait`, `escalate` or `downgrade_to`. |
| `roles.R.downgrade_to` | Required with (and only with) `downgrade_to`: the profiles the retry may use. |
| `roles.R.family_rule`, `fixed` | Recorded; family exclusion is enforced by the existing reviewer/tester `--builder-family` rule. |
| `runtug_role_map` | Seat role → routing role. A seat role it does not list is not filtered. |

Unknown fields are ignored. A duplicate key, an unknown `on_exhaustion`, a
`downgrade_to` without its mode, or a role map naming an unknown role makes
the document unreadable.

### Which role

The spawn's `--role` maps through `runtug_role_map`. `--routing-role
lead|implementer|explorer|verifier|reviewer|judge` on `spawn` or `plan`
overrides the map for that one call; the seat role (`--role`) still decides
seat admission and the family rule.

### Source and cache

Each selection runs `handoffkeep doc get policy/model-routing`. A good read
is written to `$XDG_STATE_HOME/runtug/model-routing.json` (default
`~/.local/state/runtug/model-routing.json`) with its version, replaced
atomically only when the policy changed.

| hk | Cache | Result | `policy_source` |
| --- | --- | --- | --- |
| readable | — | the hk table | `hk` |
| unreachable or unreadable | readable | the cached table, with a warning | `cache@v<N>` |
| unreachable or unreadable | none | `lead` and `verifier` refused (exit 70); every other role runs unfiltered | `builtin_default` |

Without a table, the built-in map above decides which seat roles are `lead`
or `verifier`, so a builder is still refused.

### Exhaustion

After the role filter, a role's candidates are either dropped statically
(grade, gate class, seat, effort) or by quota (`projected_exhaust`,
`window_exhausted`, `quota_unknown`, `no_presence`, `gate_refused:*`). When
nothing is admitted:

| Case | Result |
| --- | --- |
| No candidate passed the static filters | `role_no_candidate`, exit 77. Never waits or downgrades. |
| All quota-dropped, `wait` | `role_exhausted_wait`, exit 86, naming the earliest known reset of the role's pools. |
| All quota-dropped, `escalate` | `role_exhausted_escalate`, exit 87: ask the parent lane. |
| All quota-dropped, `downgrade_to` | Selection reruns once with only the `downgrade_to` profiles (grade still applies) and stamps `downgraded_from`. If those are exhausted too: `role_exhausted_wait`, exit 86. |

Selection never silently uses a profile outside the role.

### Record

The selection record (`plan --json` `selection`, `spawn --dry-run`, the
`job.placement` event) carries a `routing` object:

```json
"routing": {
  "routing_role": "lead",
  "routing_version": 1,
  "policy_source": "hk",
  "allowed": ["opus", "fable", "codex-sol", "codex-astra"],
  "on_exhaustion": "wait",
  "effort": "high"
}
```

plus `downgraded_from` and `before_downgrade` (the role's own exhausted
candidates) after a downgrade, and `refused` with the reason after a role
refusal. Rows outside the role are dropped with reason `role_filtered`. The
routing stamp sits in its own object because the record's top-level
`policy_source` already names the pool-order document.

### `runtug resolve`

`runtug resolve --role lead|implementer|explorer|verifier|reviewer|judge
[--grade G] [--builder-family F] [--json]` runs the routed selection core —
the same code `plan` and `spawn` run, including the quota gates — and prints
the chosen profile and effort. It places no host, spawns nothing and writes
no job or hk record. `--grade` defaults to `C`. The seat role is `worker`,
or `reviewer` for the `reviewer` and `judge` roles, which need
`--builder-family` (the author's or parent's family). The answer is printed
last: the text form ends with `profile=<catalog profile> effort=<effort>`,
and `--json` prints an object whose last fields are `profile` and `effort`
(plus `seat`, `model_id`, `pool`, `tier`, `rung_effort`, the routing stamp
and the full `selection` record). `effort` is the role's starting effort when
the table sets one, else the chosen rung's. A role refusal keeps its exit
code (70, 77, 86, 87); usage errors exit 2.

## Delegation

`runtug delegate` is the one-call form of "ask another model": a lead session
(Claude Code, Codex) hands over reading, verification or a cross-check and gets
one answer back. In-session subagents run only on their own vendor's models;
`delegate` reaches any seat in the policy, so the work can run on free or
spare-quota models. It reuses the spawn, wait and job-result machinery and
writes nothing in the caller's tree.

```sh
runtug delegate --role reader|verifier|reviewer --prompt FILE [--grade G] [--cwd DIR]
  [--timeout D] [--headless] [--patch] [--panel 2..4] [--family F] [--workspace W] [--json]
```

| Flag | Meaning |
| --- | --- |
| `--role` | `reader` reads and answers, `verifier` runs only the acceptance commands in the prompt, `reviewer` reviews. Reader and verifier use `worker` seats; `reviewer` uses `reviewer` seats and excludes the caller's model family. |
| `--prompt FILE` | The task. The role preamble and the steps for filing the answer are put in front of it. |
| `--grade G` | The catalog grade the seat must cover (`C`, `B`, `A`, `A+`, `S`, `S+`; default `C`). The task title is not read. |
| `--cwd DIR` | The tree the delegate reads (default: the current directory). |
| `--timeout D` | A duration or seconds, default 15m: the pane wait, or the headless CLI call. |
| `--headless` | Run the selected seat's CLI once on this host with the brief, instead of a pane. |
| `--patch` | The delegate works in a temporary detached worktree and the result carries a patch path (below). |
| `--panel N` | N from 2 to 4: see Panel. |
| `--family F` | The caller's model family, for a reviewer outside a runtug job. Inside a job the job's own family is used. |
| `--workspace W` | The herdr workspace for the pane, or `[local] workspace`. Not needed with `--headless`. |
| `--json` | Print one JSON document instead of text. |

**Selection.** The seat is chosen by the same code as `spawn` auto-selection
and `runtug plan`: catalog rungs that cover the grade and the role, ordered by
the pool policy (waste-risk pools first, then the tier order), admitted through
presence and the quota gate. Free models are one tier of that policy, not a
default target. A reviewer drops every seat of the caller's family.

**Job record.** Each delegation is a job: `delegate-<parent>-<n>`, where
`<parent>` is the enclosing job (`ARBITER_JOB`), and `<n>` the next free number
in the local jobs dir. Outside a job the id is `delegate_<host>_<n>`, where
`<host>` is `HUB_MACHINE_ID`, so two hosts never hand out the same id to hk;
without a node id the delegation refuses. It is claimed in hk and the local ledger,
holds a quota lease, carries the parent job, task and owner lane, and ends with
`done`, so placement, quota and hk records match any other job. A task is
required: run inside a runtug job or set `HK_TASK_ID`. The job directory holds
`brief.md`, `result.json`, `report.md` and, with `--patch`, `changes.patch`.

**Pane mode (default).** `delegate` runs `spawn --wait` with automatic
selection (the pane may be placed on another host), reads the `result` of the
hk job record, which the delegate files with `runtug done JOB --result`
(`{"answer": TEXT}`), then closes the pane after checking it is still the one
the job spawned. If only the report exists because the completion watcher
finished the job first, the report text is the answer. A delegate placed on
another host is answered the same way; its pane is left for that host's `reap`.
When the wait fails (escalated, lost, timeout), the pane stays for
`runtug reap` and nothing is printed on stdout.

**Headless mode.** The seat's CLI runs once, in `--cwd`, with the brief and a
deadline of `--timeout`; its output is the answer. The job is claimed, leased
and completed with the same `done` call. Kinds with a one-shot form: `claude`,
`codex`, `devin`, `cmd`, `pi`, `grok`, `opencode`; any other kind is refused
with exit 70 before anything is claimed. The one-shot flags are fixed in code
and have been exercised only against a fake runner.

**No writes.** The role preambles forbid editing: `reader` and `reviewer` run
read-only commands, `verifier` runs only the acceptance commands it was given.
A role that must try a change uses `--patch`: delegate adds a detached worktree
of `--cwd` in a temporary directory (at `HEAD` plus the caller's uncommitted
tracked changes), runs the delegate there, writes `git diff` against that base
to `changes.patch` in the job directory and removes the worktree. The result
carries `patch` (omitted when nothing changed). The caller decides whether to
apply it (`git apply`). The worktree registration in `--cwd`'s `.git` is added
and removed; no file in the tree changes.

**Panel.** `--panel N` picks N seats of N distinct model families, then runs
all N at once, each as its own delegate job (pane or `--headless`, and its own
scratch tree with `--patch`). Families are chosen one selection at a time with
the families already chosen excluded, so the panel is the N best admitted
families under the normal policy. When fewer than N are admitted, the panel
runs with those and says why (`reason`, and a warning on stderr). A member that
fails, is lost or times out does not stop the others; it is listed as `failed`
with its exit status. The panel tells each model to file `verdict` (one word)
and `points` (short claims, `file:line` where they have one) next to its
`answer`; without points, lines of the answer are compared. The table lists:

- **agreements** — points two or more members made (word overlap of at least
  half);
- **disagreements** — differing verdicts, and `file:line` locations that members
  cite with different claims;
- **unique** — points only one member made.

**Output.** The answer is the last thing printed. Text: a header line
`delegate job=… role=… mode=… profile=…`, `patch=PATH` when there is one, then
the answer. Spawn and wait chatter goes to stderr. `--json` prints one object:
`job`, `role`, `mode` (`pane` or `headless`), `profile`, `family`, `patch`,
`result` (the filed object) and `answer`. A panel prints a header line, each
member (`== A job=… profile=… family=… status=answered ==` and its answer),
then `== side by side ==` with verdicts, agreements, disagreements and unique
points last. Its `--json` object has `requested`, `answered`, `reason`,
`members` (each the single-call object plus `label`, `status`, `exit`, `error`)
and `table`.

**Exit statuses.** 0 the answer was printed (a panel: at least one member
answered; check `answered` and `reason` for a short panel). 2 usage. 70
configuration, or a seat kind with no headless form. 75 the parent job is
unreadable. 77 or 81 no seat or host was admitted (a panel with no admitted
family at all). 83, 84, 85 the pane job escalated, was lost or timed out. 124
the headless CLI hit `--timeout`. 1 the job finished without an answer, or
the headless CLI failed. Spawn and quota refusals keep their own statuses. A
panel with no answered member returns the first failed member's status.

**When to delegate.** Delegate reading, verification and cross-family review.
Keep tight design loops, where the next step depends on the last answer, in
the session's own subagents.

`plan` also prints the remote-spawn preflight (below) of every remote host it
evaluated, as `preflight host=NAME ok=… reasons=…` lines and, with `--json`, a
`preflight` array. It is informational and never changes the plan's exit status.

### `runtug preflight`

`runtug preflight [--host NAME] [--pool P] [--json]` answers "would a remote
spawn to this host work" before one is tried. Six remote-spawn failures each
needed their own fix; the preflight reports all of them per host, each with a
named reason. It is read-only: it reads the hub host list, hk presence and the
shared observation, and makes one SSH probe per host that tests two paths,
reads one boolean from the remote `hosts.toml` and asks `runtug` and `panewire`
for their versions. It never prints a URL or a file's contents, and writes no
task, job or event.

| Check | Fails with | Meaning |
| --- | --- | --- |
| `hub_acceptance` | `hub_unavailable`, `not_listed`, `not_accepting`, `disconnected` | The hub lists the host, it is connected and not `not_accepting`; the detail carries the hub's reason string. |
| `quota_observation` | `POOL:no_usable_presence`, `POOL:degraded`, `POOL:not_observed`, `POOL:stale`, `presence_unavailable` | Per pool the host's presence names (or `--pool`), the shared observation of its account exists and is no older than `fresh_ttl_s`; the detail shows `age=` against `fresh_ttl=`. |
| `quota_transport` | `hk_url_unset`, `hk_url_invalid`, `plaintext_non_tailnet`, `plaintext_tailnet_not_allowed`, `remote_config_missing` | With an `https` hk URL nothing is needed. A plain-`http` tailnet URL needs `[quota] allow_plaintext_tailnet = true` in the remote `hosts.toml`. |
| `hub_load` | `load_unknown`, `memory_pressure`, `load_over_threshold` | The hub has a load reading for the host (its metrics source lists it), reports no memory, swap or pressure reason, and the load is under `placement.load_threshold`. The memory match is on the hub's reason string and is a heuristic. |
| `review_config` | `absent` | `~/.config/mergewake/config.yaml` exists on the host (existence only; the file is not read). |
| `version_skew` | `runtug_skew`, `panewire_skew`, `runtug_missing`, `panewire_missing`, `*_local_unknown` | `runtug --version` and `panewire --version` on the host equal this machine's. |

A host that cannot run the probe (`unreachable`, `via_hub_unsupported`,
`invalid_ssh_target`, `probe_failed_rcN`) fails the checks that need it
(`quota_transport` when the URL is plain http, `review_config`,
`version_skew`).

The text output is one `preflight host=NAME ok=… [reasons=check:reason,…]`
line and one indented `check NAME ok|refused reason=… detail=…` line per check.
`--json` prints `{"mode": "preflight", "ok", "hosts": [{"host", "ok",
"checks": [{"name", "ok", "reason", "detail"}]}]}`. Exit 0 when every checked
host passes, 78 when any fails, 2 for usage or an unknown `--host`, 70 when no
remote host is configured.

`--host auto` runs the same preflight for each remote candidate it refused and
records it on that candidate's row of the `job.placement` record as
`preflight: {ok, reasons, failed}`. A host the quota gate refused was never
contacted, so its SSH-probe checks are `skipped` and only the hub and hk
checks report. When no host accepts, the reasons are also appended to the
placement error, because a failed spawn writes no placement record.

## `[artifacts]`

| Key | Default | Meaning |
| --- | --- | --- |
| `drive_folder` | required for move | Absolute or HOME-relative Drive root; destination must be beneath it as host/name. |
| `fpbulk` | required for apply | Executable accepting `status`/`evict` with newline-separated paths on stdin. |
| `exclude` | required for move | Glob array; built-in credential/.git exclusions also always apply. |
| `batch_bytes` | 3 GiB | Maximum bytes per batch, except one oversized file may occupy its own batch. |
| `batch_files` | 2000 | Maximum files per batch. |
| `poll_seconds` | 30 | Positive upload-status polling interval. |
| `confirm_timeout_seconds` | 21600 | Total batch-confirmation window; must be at least confirm_gap_seconds. |
| `confirm_gap_seconds` | 120 | Two successful fresh reads must be separated by at least this duration; values below 120 refuse. |
| `stale_minutes` | 10 | Newest DriveFS log must be within this freshness window. |
| `drivefs_log_dir` | required for apply | DriveFS log directory. |
| `app_check` | `pgrep -x 'Google Drive'` | Operator-owned shell predicate proving the Drive client runs. This is the only configured shell predicate. |
| `scratch_dir` | `~/.local/share/fleet-upload/scratch` | Temporary per-batch remote staging; removed after the batch or on error. |
| `free_floor_bytes` | 20 GiB | Space that must remain after remote batch staging. |
| `local_delete_at` | `drive_confirmed` | Sweep hatch: `drive_confirmed`, or `uploaded` for uploaded nonevidence rows without a mirror requirement. |
| `state_dir` | legacy | Retired uploader state is left untouched. Runtug uses handoffkeep records. |
| `alert_lane` | legacy | Existing value is retained; runtug returns explicit confirmation failure instead of sending uploader alerts. |

Artifact policy is the embedded
[artifact-policy.json](../internal/runtug/artifact-policy.json), or `sweep --policy`.
Its compatible schema names relative work roots and ordered classification rows
(`class`, `root`, `glob`, optional `host`, `job`, `keep_newest`, `mirror_required`).
Only evidence/final-build classes are sweep candidates; all other classes and
unmatched files are retained. `keep_newest` is accepted for policy compatibility
and cannot widen sweep eligibility. This version limits deletion to the selected
job's own inbox subtree even if a wider root is configured.

## Policy files and environment

`RUNTUG_POLICY` selects the seat-policy JSON. Each profile requires `canon`,
`kind`, `family`, `capability` and nonempty `roles`. Optional `effort` and `pin`
fix an alias to a rung; `e6` requires an exact marker. A nonempty `rungs` map is
closed: unlisted efforts are refused. A rung has `roles` and optional `e6`.
Roles are worker, builder, tester, reviewer and counsel. Model IDs and grades
are read from the quota tool, never this file.

`RUNTUG_BRIEF_POLICY` overrides `[local].brief_policy`. Brief policy schema:

```json
{
  "version": 1,
  "default": "fleet-v1",
  "blocks": { "fleet-v1": "Short host safety instructions", "host-strict": "Host-specific safety instructions" },
  "hosts": { "host-a": "host-strict" },
  "profiles": { "example-profile": "fleet-v1" }
}
```

Use the complete shipped block when creating a custom policy. Selection order
is default → host → profile → local.brief_preamble. Missing block names refuse.
The policy version and selected block appear in every brief and dry-run output.
See [DESIGN.md](../DESIGN.md#standard-brief-preamble-the-single-source-of-fleet-rules)
for ownership and distribution.

| Environment | Meaning |
| --- | --- |
| `RUNTUG_POOL_POLICY` | Overrides the pool-order policy used by `spawn` automatic model selection; `[local].pool_policy` takes precedence. |
| `HERDR_BIN`, `PANEWIRE_BIN` | CLI executable overrides. |
| `HANDOFFKEEP_BIN` | Must resolve to the canonical current-user ~/.local/bin executable in production. |
| `XDG_DATA_HOME` | Data home of the local ledger (`$XDG_DATA_HOME/arbiter/state.db`); production guard requires the canonical `~/.local/share`. |
| `XDG_STATE_HOME` | State home of the role routing cache (`$XDG_STATE_HOME/runtug/model-routing.json`); default `~/.local/state`. |
| `RUNTUG_QUOTA_BIN` | Quota executable override; default `pacelever`. |
| `RUNTUG_NODE_ENV` | Panewire node env file for `HUB_MACHINE_ID` (job record host id); default `~/.config/panewire/hub-node.env`, or `[quota].node_env`. |
| `ARBITER_INBOX_ROOT` | Jobs root; production guard requires the canonical inbox. |
| `HERDR_SESSION` | Herdr session passed through or selected for remote execution. |
| `RUNTUG_E6_ARM` | Exact canon@effort experiment marker when policy requires one. |
| `ARBITER_JOB`, `HK_TASK_ID` | Calling job/task inheritance; populated in spawned panes. |
| `RUNTUG_LANE_STRICT=1` | A committed builder with unregistered lane returns 82. |
| `RUNTUG_COMPLETION_TIMEOUT_S` | Positive watcher lifetime; duration or bare seconds, default 1h. Expiry retains the job. |
| `MOCK_MCP_PROFILE`, `RUNTUG_MOCK_MCP_CONFIG` | Required reviewed mock profile name and exclusive stdio mcpServers JSON. |
| `KIMI_CODE_HOME` | Kimi config home; thinking effort is verified. |
| `CLAUDE_CONFIG_DIR`, `CODEX_HOME` | Harness config homes for selected-workspace trust; mock Codex uses its private job home. |
| `KIMI_CODE_LOW_HOME`, `KIMI_CODE_HIGH_HOME`, `KIMI_CODE_MAX_HOME` | Pinned clone homes; default ~/.local/share/kimi-code-EFFORT. |

Legacy periodic prune environment switches are not read. Runtug never
silently enables automatic pruning.

## Local job ledger

Since RT-arb the host-local ledger lives in runtug (`internal/arbiter`); runtug
no longer executes the agent-skills `arbiter` script, and `ARBITER_BIN` is not
read. hk job records hold the cross-host job state; the ledger keeps what they
do not replace:

| Part | What it holds |
| --- | --- |
| Jobs | One row per job claimed on this host (`claimed → leased → released`), written with its `job.claim`/`job.reclaim` event. The duplicate arbiter on this host; cross-host duplicates are refused by the hk claim first. |
| quota_pool records | One execution record per job and pool, written at admission and deleted at `done`, `reap` or a failed spawn. No TTL. |
| path, linear_permit leases | Exclusive leases with a TTL (90 and 30 minutes by default), a monotonic fencing token, archived history and expiry by `gc`. Library only: runtug itself takes no such lease today. |
| Job events | `job.*`, `lease.*` and `quota_pool.*` events, one artifact each. |
| gc | `runtug gc [--json]`: expires leases, cleans quota_pool records and releases leased jobs whose exact agent label is gone from Herdr. |

State is read and written in place; no migration or import is needed:

| What | Path |
| --- | --- |
| Database (SQLite, schema version 2) | `$XDG_DATA_HOME/arbiter/state.db`, default `~/.local/share/arbiter/state.db` |
| Event artifacts | `<jobs root>/<job>/events/NNNNN-<kind>.json`, jobs root `~/work/herdr-inbox/jobs` or `ARBITER_INBOX_ROOT` |
| Events lock | `<jobs root>/<job>/.wrk-events.lock` |

The script and runtug may share one database: both write with `BEGIN
IMMEDIATE`, a 10 s busy timeout and the same events lock, and artifact numbers
continue after the highest existing prefix. A schema version other than 2 is
refused (exit 6 from the ledger, no migration). The script stays installed for
its remaining readers (for example the quota CLI's `arbiter status --json`).

Behaviour differences from the script:

- **No local claim is needed to record an event.** A job placed on another host
  has no jobs row here; its `job.placement` event is recorded anyway, which ends
  the `placement decision record failed: arbiter: job not registered` warning on
  a remote spawn. Claim event kinds are still refused, and the first event
  creates `state.db` if it does not exist (the script created it only on a claim).
- **The pool comes from the gate output runtug just received.** The profile
  named there must match and conflicting audit tokens are refused, as before,
  but the second cross-check of the pool against the quota tool's provider
  list (`scopefuel --json`) is not repeated. The gate output is passed in
  memory, not through a temporary file.
- Event artifacts are the same JSON objects with sorted keys; the separators are
  compact (`,` and `:`) where the script wrote `, ` and `: `.
- A read of the quota records of a ledger that does not exist yet answers none
  (the script failed with exit 6), so `done` and `reap` on a job that never ran
  here do not fail.
- **A job is released only when it holds nothing.** Releasing one quota_pool
  record or lease leaves the job `leased` while it still has another record or
  an unexpired lease (the script marked it released at once), so it cannot be
  reclaimed while a pool is still consumed. An expired lease holds nothing.
- **A new ledger is owner-only** (state directory 0700, `state.db` and its
  `-wal`/`-shm` 0600) because the database holds the full fencing tokens. A
  ledger that already exists keeps its modes; tighten it with `chmod 700` on
  `~/.local/share/arbiter` if it was created with the script's default.
- Failures keep the script's exit statuses (3 denied, 4 duplicate, 5 fencing,
  6 storage, 7 not found) and surface as `arbiter <operation>: ...`.
- The trust guard no longer checks an `arbiter` binary (there is none to run);
  `HOME`, `ARBITER_INBOX_ROOT` and `XDG_DATA_HOME` must still select the
  canonical paths. `ARBITER_TEST_NOW` is not read; tests inject a clock.
- Not ported: the script's own command line. Operators keep using the script for
  `status`, `job-get`, `lease`, `release` and `event` by hand.

## Exit statuses

| Code | Meaning |
| --- | --- |
| 0 | Success or preview. |
| 1 | Generic failure: no, ambiguous or multiple `find` matches; restore receipt failure after reclaim. |
| 2 | Usage/task/policy target refusal. |
| 3 / 4 | Quota-tool refusal, unchanged; artifacts use 3 for upload confirmation timeout and 4 for unconfirmed Drive eviction. |
| 5 / 6 | Hub quota denial / authentication refusal. |
| 70 | Configuration, trust or unverified admission evidence. |
| 74 | Active job duplicate, including an hk `job_id_taken` claim refusal. |
| 75 | Unreadable duplicate state (the ledger's record of a duplicate job has no state or an unreadable `job.spawned` receipt; a released job has no receipt and is reclaimed), changed artifact, or a `--parent-job` that resolves neither locally nor in hk within the bound. |
| 76 | Initial brief submission unconfirmed after one key retry. |
| 77 | Role/seat/grade/family/E6 refusal; quota gate exit 5 is remapped here. |
| 78 | `preflight`: at least one checked remote host failed a check; the reasons are on stdout and in the error. |
| 79 | Mock isolation refusal. |
| 80 | Local spawning disabled/unavailable without a remote fallback. |
| 81 | Wake/capacity placement failure, the remote worktree for a linked-worktree job could not be prepared (also a `via = "hub"` checkout refusal), or the hub is unavailable for a forced `via = "hub"` host. |
| 82 | Builder pane committed but lane unregistered under strict lane reporting. |
| 83 | `spawn --wait` (and `delegate`): the job escalated. |
| 84 | `spawn --wait`: the job was lost or revoked. |
| 85 | `spawn --wait`: the wait deadline expired; the job is retained. |
| 86 | Role routing: every profile the role allows is quota-exhausted and its `on_exhaustion` is `wait` (`role_exhausted_wait`, naming the earliest known reset), or its `downgrade_to` profiles are exhausted too. |
| 87 | Role routing: every profile the role allows is quota-exhausted and its `on_exhaustion` is `escalate` (`role_exhausted_escalate`): ask the parent lane. |
| 124 | A time-bounded CLI call reached its context deadline. |
| 255 | SSH could not reach a remote host (repository check, clone or spawn leg). Never returned for a `via = "hub"` host. |

Unexpected dependency failures preserve their subprocess exit code. A pane
success receipt is always authoritative for avoiding a second spawn, even with exit 82.
