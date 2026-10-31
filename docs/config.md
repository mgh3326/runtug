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
| `via` | `ssh` | `ssh` or legacy `hub`; legacy hub spawn is refused until the hub can invoke runtug. |
| `herdr_session` | `workers` | Remote HERDR_SESSION. |
| `workspace` | spawn `-w` | Remote workspace. |
| `cwd_map` | explicit override | Inline table of local prefix → remote prefix; longest prefix wins. Only needed as an exception: the default remote cwd is `$HOME/work/<repo>` on the target, cloned when missing. For a job run from a linked git worktree the default is `<work root>/<worktree dir>`, where the work root is derived from entries that map a checkout directly under the local work root (else `$HOME/work`). |
| `cwd_keys` | legacy | Kept for legacy hub configurations; not used by SSH. |
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
re-reads the shared observation and judges it. A failed, timed-out or
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
host}` runs before the arbiter claim and a taken id refuses with exit 74, like
the arbiter's duplicate. `host` is the node's `HUB_MACHINE_ID`. hk unreachable
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
[--allow-escalation] [--json]` answers "right now, which host and which
model would a spawn get" without a task or brief. It is live and read-only:
it runs the spawn's selection core (catalog, pool status, presence and each
host's quota gate) with the grade from `--grade` instead of a task title,
then walks the same placement plan for the chosen seat — the local launch
resolution, seat check and gate for this host, the shared-account quota
gate for each remote host — and stops at the first host that admits it. It
never spawns, wakes a host, claims an arbiter job, writes an hk task or job
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

A local launch or gate refusal keeps its own code, as in `spawn`.

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
| `HANDOFFKEEP_BIN`, `ARBITER_BIN` | Must resolve to the canonical current-user ~/.local/bin executable in production. |
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
| 75 | Unreadable duplicate state (an `arbiter job-get` reply without a state, or whose `receipt` is not an object, the string `"absent"`, null or missing; `"absent"` on a released job means no receipt and the job is reclaimed), changed artifact, or a `--parent-job` that resolves neither locally nor in hk within the bound. |
| 76 | Initial brief submission unconfirmed after one key retry. |
| 77 | Role/seat/grade/family/E6 refusal; quota gate exit 5 is remapped here. |
| 79 | Mock isolation refusal. |
| 80 | Local spawning disabled/unavailable without a remote fallback. |
| 81 | Wake/capacity placement failure, or the remote worktree for a linked-worktree job could not be prepared. |
| 82 | Builder pane committed but lane unregistered under strict lane reporting. |
| 83 | `spawn --wait`: the job escalated. |
| 84 | `spawn --wait`: the job was lost or revoked. |
| 85 | `spawn --wait`: the wait deadline expired; the job is retained. |
| 124 | A time-bounded CLI call reached its context deadline. |
| 255 | SSH could not reach a remote host (repository check, clone or spawn leg). |

Unexpected dependency failures preserve their subprocess exit code. A pane
success receipt is always authoritative for avoiding a second spawn, even with exit 82.
