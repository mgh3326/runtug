# Design

runtug owns launch and supervision. It orchestrates existing CLIs through a
`Runner` boundary; unit tests substitute fakes for all external services.
`QuotaGate` separates profile launch resolution from quota admission, so the
quota implementation stays behind the pacelever CLI contract.
Models, effort and grades always come from `policy launch PROFILE --effort E
--role R --json`. A missing or malformed catalog answer refuses admission.

## State and transaction boundaries

Arbiter is the only job and lease ledger. Runtug calls `claim`, `job-get`,
`lease`, `release`, `status` and `event`. It reads arbiter's exported event
artifacts, ordered by event timestamp, then file identity, for task inheritance
and cleanup. It creates no job-state database and never edits arbiter's ledger.
The inbox default is `~/work/herdr-inbox/jobs`.

A local spawn validates flags and task identity, reads the task, resolves the
catalog and policy, runs quota admission, claims and leases through arbiter,
creates a pane and launches the harness. The brief is submitted and verified
before handoffkeep's atomic `tasks claim --by LABEL --job-id JOB` response is
accepted. Claim responses missing either field fail closed. Inherited children
do not reclaim the parent's task. A readable refusal cannot be bypassed with
`--task-hk-bypass`; an availability override is recorded in arbiter metadata.

Pane creation arms failure cleanup. Failure captures a private screen artifact,
closes that pane and releases the lease. The durable `job.spawned` event commits
the run. A duplicate live claim exits 74, and unreadable duplicate state exits
75. `--job-dup-ok` is explicit and audited. Quota gate role denial (exit 5)
becomes 77; quota exits 3/4 and hub quota exit 5 retain their meanings.

Canonical task/lease binaries, account home and ledger roots are checked before
production operations. Executables must belong to the current uid and must not
be group/world writable. Tests use injected runners and temporary roots;
there is no environment variable that disables the production trust guard.

## Policy data

[policy.json](internal/runtug/policy.json) contains profile aliases, harness kinds,
provider families, capability keys, pinned efforts, permitted roles and E6
markers. Optional per-effort `rungs` override roles and marker requirements.
There are no model IDs or grade assignments in this file or the launcher code.
The quota catalog owns those changing values. A policy can require the special
Codex footer proof through `proof: "codex-footer"`.

Generic enforcement compares the requested task grade to the canon grade,
requires independent builder/tester families, checks host capabilities and
refuses absent data. pi's family is anthropic. Command Code and the Devin ds41
seats both use deepseek, regardless of their harness or quota pool. pi and cmd
builder seats are allowed on default rungs; consult_only and escalation rungs
remain leaf seats. The upstream quota gate can still impose a stricter refusal;
runtug never hides a role denial to open a seat.

A catalog rung with an empty effort is the profile's default rung. A seat whose
launcher does not pass an effort (agy, devin, opencode, and a routed `cc-*`
claude harness) may launch it: the launcher adds no effort flag and the quota
gate is called without `--effort`. A seat that declares an `effort`, or whose
launcher passes one (claude, codex, grok, pi, cmd, kimi, kiro), refuses it, so
those rows stay out of auto selection. A seat's optional `gate` names the quota
gate profile when it differs from the launch canon (every agy spelling is gated
by the one `agy-flash` pool gate); because that gate cannot see an escalation
rung, an aliased seat on a non-default launch row needs `--allow-escalation`.
A seat's `route` (`clinepass`) with `context_tokens` redirects a claude-kind
harness; the key file is read at spawn time and handed only to the pane.

`standard` is the policy sentinel for harnesses with no special host capability.
API-backed pi and cmd seats require literal `pi_anthropic=true` or
`cmd_dsflash41=true`; routed `cc-*` seats require `clinepass=true`. Unknown capabilities are denied. E6 aliases require the
exact `RUNTUG_E6_ARM=CANON@EFFORT` marker unless their policy row says otherwise.

## Standard brief preamble: the single source of fleet rules

[brief-policy.json](internal/runtug/brief-policy.json) is the versioned source for
the short host-safety block prepended to **every** spawned brief. Fleet rules
live here, replacing per-repository notes and manually repeated brief blocks:

- No local database servers; database integration runs in CI.
- Run tests for changed code locally; full suites belong in CI.
- Stop fixture/background processes by their PID before finishing.
- Work in a worktree, never the canonical checkout.

The preamble is instructions to the spawned agent, not an OS sandbox. Host and
profile selectors choose a named block from policy data; unknown selections
fail closed. Precedence is default, host, profile, then explicit host config
`brief_preamble`. `RUNTUG_BRIEF_POLICY` or `[local].brief_policy` can select an
operator-maintained copy of the same schema. Keep changes versioned in runtug;
local paths are distribution mechanisms, not a reason to duplicate rules.

`spawn --dry-run` prints the selected host, profile, policy version, block name
and complete composed brief. It creates no pane, lease, task claim, mock config
or watcher. Local preview does not call dependency CLIs. Auto placement may
perform its read-only placement query. A remote execution host composes the
preamble from its own deployed policy; keep host policy versions synchronized
and compare the remote preview before enabling it. The caller transports the
original brief, preventing double preambles.

## Harness startup and submission

Claude, Codex, Devin, Grok, Kimi, pi and Command Code share the launch shapes runtug inherited.
The catalog supplies model IDs; Kimi's effort must match its clone-home config.
Devin and cmd use `pane run` and detection/rename; other harnesses use
`agent start --kind`. Before launch, an exact output token proves that the
shell executes commands. Foreground process inspection prevents a brief being
typed into a shell after a missing executable. Devin additionally requires its
welcome identity rule. pi's footer and cmd's welcome block attest model and
effort; their text cannot be a copy of the brief. cmd accepts both absolute and
HOME-abbreviated `# ~/...` cwd rows and rejects conflicting model rows.

Workspace trust is merged in the harness's existing store under its stable
lock, with atomic file replacement. Malformed and symlinked stores refuse.
Devin requires a provisioned trusted_paths store; Kimi uses its selected clone
home. The selected workspace alone is trusted. Codex's TOML keeps existing
content and appends only an absent project entry.

Initial delivery starts from an idle pane. Command Code receives `pane send-text`
then `pane send-keys ... return`. Other harnesses use `agent prompt`.
Submission requires a subsequent working status with no pasted-text chip.
Pasted content, a transcript echo, an accepted API response and queue banners
alone cannot produce `landed=yes`. On timeout, runtug sends the submit key once
more and checks status again; it never blindly pastes a second copy. Failure
returns 76 and cleans the uncommitted pane. Very fast completed agents can be
conservatively refused if their working transition was not observed.

Mock mode supports exclusive stdio descriptors for Claude/Codex. Unsupported
harnesses and remote mock placement return 79. Claude uses strict MCP config;
Codex uses a private per-job home and refuses conflicting project MCP config.
Runtug itself starts no mock server.

## Completion and cleanup

`done` serializes per job, deduplicates report content and snapshots changed
reports that reuse a path. It uploads a report document, records completion
through arbiter, relays through `panewire emit`, comments on the task and
releases quota. An unavailable relay leaves a durable `job.relay_failed` event
and `relay=file-only`. Escalation routes only to the recorded parent.
`restore` reclaims a released job only after verifying its existing pane, name
and tab; it does not create a second pane.

A detached internal watcher accepts a stable report over two observations and
uses the same done path. Watch expiry retains the live job and quota. Two
observations of a missing pane record loss and release quota. An exclusive
watcher lock prevents multiple watchers for a job. There is no public sentinel
state store and no periodic cleanup daemon.

Reap/prune are previews unless `--apply`. Kept jobs remain protected. Builders
require `--include-builders`. A newer claim/reclaim/spawn defeats an older
completion. Reap rechecks current identity and never closes a working pane or
an entire unverified split tab. Lane removal uses expected machine and pane
conditions. Optional worktree removal requires a clean linked worktree whose
HEAD is merged into origin/main and uses git without force.

Artifact move copies/hash-checks files in bounded batches, requires two fresh
Drive upload confirmations at least 120 seconds apart, records them through
handoffkeep, then evicts Drive copies. Original files remain. Remote pulls use
read-only inventory and rsync file lists, with a free-space floor per batch.
Sweep uses the compatible `fleet.artifact.v2` records and
`fleet.artifact-policy.v1` classification schema. It requires task/job/host
binding, archive confirmation and a matching size/hash immediately before
deletion. Metadata is recorded before removal; repeat runs safely skip missing
files. It never removes unclassified files, credentials or source symlinks.

## Determinism and code conventions

Identical inputs must produce identical decisions, output and errors. The
package keeps to these rules; the tests named here enforce them:

- Never let map iteration order reach output, records, errors or decisions.
  Iterate `slices.Sorted(maps.Keys(m))` instead; validation then reports the
  first invalid entry in name order.
- Sort with `slices.SortStableFunc` and a comparator that is a strict weak
  order (compose `cmp.Compare` results) ending in a deterministic tie-break
  such as a name or configuration order.
- Read the clock through `App.Now` and sleep through `App.Sleep`.
- Exit statuses are the named `exit*` constants in `command.go`; `fail` never
  takes a literal number (`TestExitCodesNamed`), and each constant has a row
  in the [exit status table](docs/config.md#exit-statuses)
  (`TestExitCodesDocumented`).
- Tests are hermetic: `TestMain` clears every environment variable runtug
  reads, and `TestEnvReadsAreHermetic` fails when a new variable is outside
  that list. Set variables in a test with `t.Setenv`.

## Parity and migration from the retired spawner

| retired feature | runtug v0.1 status |
| --- | --- |
| Core spawn flags, roles, owner, task/parent binding | Implemented; new `--dry-run` preview |
| Profile catalog, grade/effort, E6, seat policy | Canon resolution plus versioned policy data; no model fallbacks |
| Local quota / hub quota / role exit 77 | Implemented; fail closed on missing evidence |
| Local and SSH placement; pressure and wake request | Implemented as a local policy engine: capability `--tags`, presence/quota reads (degraded = unknown), hub load5/ncpu ordering, `$HOME/work/<repo>` clone convention (plus a matching remote `git worktree` for linked-worktree jobs) with `cwd_map` kept as explicit override, documented wake rules through a `Waker` interface, decision recorded as `job.placement` |
| `via=hub` spawn | Refused until the hub gains a runtug-capable upstream adapter |
| Harness launch and proof | Implemented for the seven core harnesses; extra catalog aliases fail closed if unavailable |
| cmd long brief submission | Fixed: explicit submit, status verification, one key retry |
| Mock lane | Exclusive Claude/Codex stdio descriptors; unsupported/remote seats refused |
| `--report` completion watcher | Implemented; stable report, loss and expiry handling |
| done / escalate / restore | Implemented; reports, deduplication, parent relay and verified recovery |
| reap / prune / worktree-reap | Implemented as previews; worktrees folded into reap |
| heavy / heavy status | Removed; placement is load-based only, no host slot lock |
| lanes-missing / hosts / profiles / find / name-sync | Implemented; ambiguous finds fail |
| artifacts move / sweep / classify / mark-final | Move/sweep implemented; classification internal; no separate mark-final command |
| usage | Not carried over; no standalone disk alert |
| rounds / o3 / joined / takeovers / grants / landing-strict | Deliberately excluded; mergewake owns review/landing |

1. Validate runtug configuration and `spawn --dry-run` beside the pinned
   spawner on each host. Do not replace the pinned binary before the hub
   transport has migrated; hub-launched jobs still depend on it.
2. Rebaseline trust receipts through the merge/release policy before changing
   skills or wrappers. The repository's mergewake policy is shadow only and
   names the exact CI checks; it authorizes no automatic merge or rollout.
3. Use isolated worktrees, fresh task/job IDs and fake/mock runs. Confirm CLI
   versions, model/footer shapes, quota policies and task binding on each host.
   Existing provisioning and trust dialogs must be resolved before unattended
   launch; an unknown UI fails closed.
4. Explicitly install a pinned release on one chosen host, then swap only the
   binary name in the core spawn/done/reap paths. Keep review/round callers on
   mergewake. No rollout is performed by this build task.
5. Expand only after comparing receipts, completion relay, quota release and
   cleanup. Retain the previous spawner for legacy hub transport until that
   boundary is migrated; arbiter remains the shared ledger throughout.

Inventory risks R1/R3/R4/R6/R9/R10/R11 remain external-service boundaries;
runtug neither removes their routes, wrappers, provider pools nor attachment
state. Named tests cover those boundaries, R2 installed-catalog resolution,
R5 shadow/pin migration, R7 hygiene/locking, R8 recovery, R12 ledger ownership,
R13 manual quota fallback, R14 mock MCP isolation and R15 exit remapping.
