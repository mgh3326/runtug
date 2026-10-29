# Configuration

Runtug reads `~/.config/wrk/hosts.toml`. `WRK_HOSTS_CONFIG` overrides the file;
otherwise `XDG_CONFIG_HOME/wrk/hosts.toml` is used when XDG_CONFIG_HOME is set.
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
heavy_max = 1
heavy_load_wait = true
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
| `heavy_max` | `1` | Concurrent host build slots; `0` refuses builds with 78. |
| `heavy_load_wait` | `true` | Literal boolean; wait for load5 / CPU below 1 before the build. |
| `shell_ready_timeout_ms` | `120000` | Startup readiness window, integer 30000–300000. |
| `pi_anthropic` | `false` | Literal capability opt-in required by pi policy rows. |
| `cmd_dsflash41` | `false` | Literal capability opt-in required by cmd policy rows. The provisioned cmd account must have no persisted MCP servers. |
| `clinepass` | `false` | Literal capability opt-in required by the `cc-*` seats, which route the Claude Code harness through the host's local ClinePass proxy (`CLINEPASS_GATE_URL`, key read at spawn time from `CLINEPASS_GATE_KEY_FILE`; defaults are a loopback URL and `~/.config/cliproxyapi-key.txt`). |
| `tags` | unset | Capability tags offered by this host for job placement (`spawn --tags`). Lowercase tag names. |
| `presence_id` | `machine_id` | Identity this host uses in pacelever host-presence records. |
| `brief_policy` | embedded policy | Path to the versioned brief-policy JSON; leading `~/` expands. |
| `brief_preamble` | selected policy block | Explicit named preamble block for this host. |
| `pool_policy` | embedded pool-order policy | Path to the versioned pool-order JSON used by automatic model selection. |
| `fleet_usage_alert_gb` | legacy/read-only | Retained config key; standalone wrk disk alerts are not emitted in v0.1. |

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
| `cwd_map` | explicit override | Inline table of local prefix → remote prefix; longest prefix wins. Only needed as an exception: the default remote cwd is `$HOME/work/<repo>` on the target, cloned when missing. |
| `cwd_keys` | legacy | Kept for legacy hub configurations; not used by SSH. |
| `tags` | unset | Capability tags offered by this host. A job's required `--tags` must be a subset for the host to be a placement candidate. |
| `presence_id` | host `NAME` | Identity this host uses in pacelever host-presence records. |
| `capacity` | unset | Inventory value displayed by hosts. Authoritative capacity comes from panewire placement and remote admission. |
| `wake` | off | `"panewire"` marks the host wakeable: the placement policy may request `panewire node wake` when asleep, then waits for the node to connect. |
| `wake_hold` | `10` | Burst hold in minutes. |
| `wake_wait` | `90` | Burst command timeout in seconds; wake refusal returns 81. |
| `brief_policy`, `brief_preamble` | host policy default | Optional initiator-side preview settings for this host; deploy matching policy on the execution host. |

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

Before trying a remote candidate (including an explicit `--host NAME`), runtug
resolves that host's most recent fresh presence for the launch pool, then asks
`pacelever gate --account-key POOL:FINGERPRINT --json` to judge its shared
observation. The spawner's `[quota.accounts]` does not pin remote accounts.
Missing, ambiguous, stale or degraded target evidence refuses that host; auto
placement tries the next candidate. Local placement keeps the local gate path.
The selection's `quota_gates` and placement candidate's `quota_gate` record the
host, account key, evidence source, outcome and exit code of each attempt.

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
| `accounts` | unset | Optional pool → 16-hex account-fingerprint table for exact account keys. |

Automatic model selection (`spawn` with `-m` omitted or `-m auto`) reads the
live bench catalog and pool status from the quota CLI, then orders candidates
under the versioned pool-order policy (`[local].pool_policy` or
`RUNTUG_POOL_POLICY`, else the embedded copy): free pool/model sets first,
then WASTE subscription pools, then named credit pools, then ranked spend
pools. Inside a tier the lowest catalog grade that satisfies the task wins,
then the pool with the lower projected use at reset (the used percentage when
a pool cannot be paced yet), then the smaller effort, then the catalog score.

The projection is pacelever's pace: for each account-scope bucket in the
quota status, used percentage ÷ elapsed share of its window, paced at the
status document's `generated_at`; a bucket past its reset or under 5% into its
window has none, and a pool's projection is its highest bucket's. Two policy
keys use it, both defaulting to 100:

| Key | Default | Meaning |
| --- | --- | --- |
| `waste_below_projected_pct` | `100` | A non-credit subscription pool projected below this at reset is WASTE: the rest is lost at reset, so it is spent before credit and ranked pools. Without a projection the quota status `verdict.waste` flag decides. An explicit `0` disables projected waste. |
| `exclude_at_projected_pct` | `100` | A pool (free pools included) projected at or above this is dropped as `projected_exhaust`, whatever its tier. Must be above 0 and not below `waste_below_projected_pct`. An over-consumed reading (used above 100%) still projects, so it is excluded. |

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
order: the policy `order` and the grade-fit rule apply to every task
(policy/quota-stop: free first, always), and urgent work reaches a paid pool
when the free rung is refused or excluded. The `job.placement` selection
record carries `urgency`, `urgency_source` (`flag`, `task-priority` or
`default`), the `tiers` order used, a one-line `decision`, the ordering
`rule` with both thresholds, `projected_pct` and `used_pct` per candidate,
and on `chosen` its `tier`, `grade`, `projected_pct` and a one-line `why`;
`--dry-run` prints `urgency=<v> source=<s> tiers=<a>><b>...` before the full
record.

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
| `state_dir` | legacy | Existing wrk uploader state is left untouched. Runtug uses handoffkeep records. |
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
| `SCOPEFUEL_BIN` | Quota executable; default `scopefuel`. |
| `RUNTUG_QUOTA_BIN` | Preferred compatible quota executable override, e.g. `pacelever`. |
| `ARBITER_INBOX_ROOT` | Jobs root; production guard requires the canonical inbox. |
| `HERDR_SESSION` | Herdr session passed through or selected for remote execution. |
| `SCOPEFUEL_E6_ARM` | Exact canon@effort experiment marker when policy requires one. |
| `ARBITER_JOB`, `HK_TASK_ID` | Calling job/task inheritance; populated in spawned panes. |
| `WRK_LANE_STRICT=1` | A committed builder with unregistered lane returns 82. |
| `WRK_COMPLETION_TIMEOUT_S` | Positive watcher lifetime in seconds; default 3600. Expiry retains the job. |
| `MOCK_MCP_PROFILE`, `WRK_MOCK_MCP_CONFIG` | Required reviewed mock profile name and exclusive stdio mcpServers JSON. |
| `KIMI_CODE_HOME` | Kimi config home; thinking effort is verified. |
| `CLAUDE_CONFIG_DIR`, `CODEX_HOME` | Harness config homes for selected-workspace trust; mock Codex uses its private job home. |
| `KIMI_CODE_LOW_HOME`, `KIMI_CODE_HIGH_HOME`, `KIMI_CODE_MAX_HOME` | Pinned clone homes; default ~/.local/share/kimi-code-EFFORT. |
| `WRK_HEAVY_LOCK` | Host lock path; default temporary-directory/wrk-heavy-HOST.lock, shared with wrk. |
| `WRK_HEAVY_MAX` | Nonnegative per-invocation slot limit override. |
| `WRK_HEAVY_CLASS` | `ci` or `lane` audit label; no priority scheduling in v0.1. |
| `WRK_HEAVY_WAIT_CAP` | Slot/load wait cap in seconds, default 1200; expiry returns 75. |
| `WRK_HEAVY_HELD_FD` | Internally inherited fd:slot-path for verified nested builds. |
| `WRK_HEAVY_LOG` | Append-only build audit log, default ~/.local/state/wrk/heavy.log. |

Legacy heavy CI/LANE defer limits, durable queue tickets and periodic prune
environment switches are not read. Runtug never silently enables automatic
pruning. `--queue-key` remains an audit label, not a durable retry reservation.

## Exit statuses

| Code | Meaning |
| --- | --- |
| 0 | Success or preview. |
| 1 | Generic failure: no, ambiguous or multiple `find`/`name-sync` matches; restore receipt failure after reclaim. |
| 2 | Usage/task/policy target refusal. |
| 3 / 4 | Quota-tool refusal, unchanged; artifacts use 3 for upload confirmation timeout and 4 for unconfirmed Drive eviction. |
| 5 / 6 | Hub quota denial / authentication refusal. |
| 70 | Configuration, trust or unverified admission evidence. |
| 74 | Active job duplicate. |
| 75 | Unreadable duplicate state, changed artifact or heavy wait cap. |
| 76 | Initial brief submission unconfirmed after one key retry. |
| 77 | Role/seat/grade/family/E6 refusal; quota gate exit 5 is remapped here. |
| 78 | Heavy configuration refusal. |
| 79 | Mock isolation refusal. |
| 80 | Local spawning disabled/unavailable without a remote fallback. |
| 81 | Wake/capacity placement failure. |
| 82 | Builder pane committed but lane unregistered under strict lane reporting. |
| 83 | `spawn --wait`: the job escalated. |
| 84 | `spawn --wait`: the job was lost or revoked. |
| 85 | `spawn --wait`: the wait deadline expired; the job is retained. |
| 124 | A time-bounded CLI call reached its context deadline. |
| 255 | SSH could not reach a remote host (repository check, clone or spawn leg). |

Unexpected dependency failures preserve their subprocess exit code. A pane
success receipt is always authoritative for avoiding a second spawn, even with exit 82.
