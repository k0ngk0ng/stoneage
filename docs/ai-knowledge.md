# AI knowledge snapshots

`internal/aiknowledge` loads the effective StoneAge 2.5 data directory into a
versioned snapshot. `Load(ctx, Options)` accepts a repository root, a `gmsv`
root, or the directory containing the tables. `LoadDataDir` and `LoadPath` are
convenience forms. `Options.TaskDir` selects a directory of task JSON files;
when it is empty, the package uses the embedded definitions.

The snapshot exposes the parsed experience, encounter, enemy, enemybase,
group, map warp, and NPC records. `Areas`, `Enemies`, and `Tasks` return copies;
`FindArea`, `FindEnemy`, and `FindTask` provide stable ID lookups. `Coverage`
reports unsupported NPC artefacts, broken references, incomplete leveling
areas, and task verification gaps. `Fingerprint` is a SHA-256 over sorted
source paths and their raw bytes, including task definitions. A changed data
file therefore invalidates a previously generated plan.

The parser follows the server's field layout. `encount.txt` is read as 33
integer fields; `enemy.txt` has three text fields followed by 31 integer
fields; `enemybase.txt` has six text fields followed by the server's 48 integer
fields; and `group.txt` has one text field followed by 23 integer fields.
The server loads the enemybase level-up value with C `atoi`, so values such as
`4.50` are retained using their effective integer value `4`. `map/mapwarp.txt`
uses `type:time:floor,x,y:floor,x,y:attribute` records. UTF-8, GBK/CP936, and
Shift-JIS are decoded explicitly; malformed bytes become an issue and are
never silently replaced by U+FFFD.

Non-strict loads retain recoverable rows and append warning issues. Strict
loads fail on malformed required rows, broken references, invalid task JSON,
or stale task evidence. Optional generator and binary NPC artefacts remain in
the file inventory with `Supported=false`; strict mode does not pretend that
those files are executable configuration.

## Executable task definitions

Task JSON is a reviewable, hand-maintained contract. Each task has an `id`,
`name`, `status` (`verified`, `unverified`, or `unsupported`), one or more
steps, task-level `success` conditions, a budget, and source evidence. A step
contains an executor `action`:

```json
{
  "skill": "npc.window",
  "arguments": {
    "npc": "riderman",
    "window_sequence": 200,
    "choice": 1
  }
}
```

`arguments` must be a JSON object containing all submission values. NPC/window
actions must state the NPC and the server's wire window sequence explicitly.
The sequence is not the `winno` used inside the legacy NPC configuration: the
2.5 `npc/npc_riderman.c` and the preserved 8.5 reference client both add 100
when sending a window and subtract 100 when receiving one. A step also states
`timeout_seconds`, `cost_known`, `maximum_cost`, and
`success_conditions`. The legacy `success` string list is allowed only as a
human annotation; descriptions and free-form expressions are never treated as
completion predicates.

Tasks may declare `dependencies: ["prerequisite-task-id"]`. `TaskOrder(id)`
returns their reachable graph in prerequisite-first order, visiting shared
dependencies once. Missing references, duplicate IDs, repeated dependencies,
self references, cycles and oversized graphs are rejected. Strict loading
fails; non-strict loading records an issue and makes affected definitions
unavailable. A verified task with an unverified prerequisite is downgraded.
The returned definitions are independent copies.

The planner checks the full graph's verification, preparation review and
evidence, then adds only direct prerequisites' completion predicates to the
requested task's entry conditions. It does not add transitive reward-item
conditions: an intermediate task may already have consumed that reward.
Dependencies must have completion state that can be observed after reconnect;
connection-local `window_submitted` completion is rejected for dependencies.
Task definition fingerprints include the dependency declarations.

Prerequisite tasks can be started through the human-facing automation controls.
Each task retains its own handle, budget and checkpoint; no prerequisite actions
or charges are hidden inside the requested task's plan. Quote and authorize
chain costs across its nodes.
Existing receipts do not replace a fresh completion observation. Task startup
rechecks all entry conditions after preflight, including dependency completion.
The two embedded quests remain unverified and have no invented dependencies.

`MachineCondition` uses `kind`, `id`, `value`, `x`, and `y`, matching the
fields of `internal/automation.Condition`. The directly transferable kinds
are `character_level`, `gold_at_least`, `not_battle`, `alive`, `position`,
`pet_level`, `item_count`, `flag_set`, and `flag_clear`. The knowledge package
also names `character_skill_level` and `window_sequence` for state that the
current 2.5 observation adapter must project explicitly before execution.
For an AI player with an authenticated `unlimited_funds` capability,
`gold_at_least` is satisfied by that capability even when the visible balance
is zero; the capability is never inferred from task JSON or model input.

Every evidence source is relative to the loaded data directory and may carry
`line`, `line_end`, and a SHA-256. A task can be `verified` only when every
source hash matches the loaded bytes, `data_fingerprint` matches the digest
of all referenced files, every static evidence claim is marked verified, and
an operator has independently set `execution_verified=true` after a runtime
test. Loading static files cannot establish runtime execution verification.
Definitions without those facts remain `unverified`; malformed or stale
definitions are `unsupported` in a non-strict load.

The embedded `tasks/riding-basic.json` is the first real-server task. It uses
the verified static `riderman.create` and `riderman.conf` records: family 1's
trainer entity is at floor 1040, `(61,45)`, and the legal interaction tile in
front of it is `(60,45)`. The executable conversation is `talk → sequence 101,
choice 4 → sequence 200, choice 1 → sequence 210, choice confirm`; choices are
the 1-based SELECT values sent by the native client. The three relevant
configuration windows have `winno` values 1, 100, and 110, which become wire
sequences 101, 200, and 210 respectively. The basic course charges 5000 石币
and its final machine condition is the server-side `learn_ride` value reaching
40. The task remains `unverified` because the preserved source tree does not constitute a live
execution test; a validated `S("AI")` observation can expose that field, but
it does not prove that the course was accepted. All static evidence assertions
remain false. The NPC object ID is assigned by the running server, so it is
checked against the runtime NPC/window contract and is not fixed in the
reusable task definition.
No task set is inferred from names, NPC file counts, or model output.

Task budgets describe the configured cost of an action. The riding task keeps
the finite 5000 石币 quote in `maximum_cost` and the task budget even for an AI
player with the server-enforced `unlimited_funds` capability. The capability
comes from the authenticated server policy and is refreshed on observations;
while it is active, automation does not pause because the visible gold balance,
reserve, or finite task budget is insufficient. The NPC executor still checks
the verified quote and the server records the charge. Revocation returns the
normal balance and budget checks. An unlimited capability must never be
represented by an overflowing task gold value, a fake `gold_cost` argument, or
by skipping action result checks.
