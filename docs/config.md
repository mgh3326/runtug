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
cwd_map = { "/home/example/work/project" = "/srv/work/project" }

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
| `brief_policy` | embedded policy | Path to the versioned brief-policy JSON; leading `~/` expands. |
| `brief_preamble` | selected policy block | Explicit named preamble block for this host. |
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
| `cwd_map` | required for SSH spawn | Inline table of local prefix → remote prefix. Longest exact, subdirectory or dotted-worktree prefix wins. |
| `cwd_keys` | legacy | Kept for legacy hub configurations; not used by SSH. |
| `capacity` | unset | Inventory value displayed by hosts. Authoritative capacity comes from panewire placement and remote admission. |
| `wake` | off | `"panewire"` requests a burst hold before remote spawn. |
| `wake_hold` | `10` | Burst hold in minutes. |
| `wake_wait` | `90` | Burst command timeout in seconds; wake refusal returns 81. |
| `brief_policy`, `brief_preamble` | host policy default | Optional initiator-side preview settings for this host; deploy matching policy on the execution host. |

`--host auto` uses panewire's eligible ordered candidates. A healthy local host
is used only when hub placement is unavailable and local fallback policy permits
it. A configured local refusal or pressure uses hosts in configuration order.
Admission errors stop a candidate round; capacity/transport failures may try the
next candidate. A remote success receipt, including partial-success exit 82,
always stops the round to avoid duplicate panes.

Runtug must already be installed on SSH targets. `_spawn-stdin` transports a
structured request and original brief through stdin, uses a private temporary
brief file and deletes it after spawn. The target composes its own preamble.
No host installation or service management is performed by spawn.

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
| 2 | Usage/task/policy target refusal. |
| 3 / 4 | Quota-tool refusal, unchanged; artifacts use 3 for upload confirmation timeout. |
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

Unexpected dependency failures preserve their subprocess exit code. Time-bounded
CLI calls return 124 when their context deadline expires. A pane success receipt
is always authoritative for avoiding a second spawn, even with exit 82.
