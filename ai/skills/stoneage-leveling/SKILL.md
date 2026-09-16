---
name: stoneage-leveling
version: 1.2.0
description: Level a bound StoneAge character or identified pet to a server-confirmed target using verified areas, controlled recovery, and a persisted handle.
---

# StoneAge leveling

Use `game_observe` first. For a pet target, use its stable identity from the
observation or the management configuration; a party slot or list position is
not a stable identity. Call `game_query_knowledge` with `kind: "leveling"` and
then `route` for candidate areas. Select only verified entries that state the
applicable level range, encounter/experience evidence, travel requirements,
and recovery or supply assumptions. Never invent a leveling area, encounter
rate, speed claim, or cost estimate.

Start the goal with `game_start_leveling`, supplying `target_kind`,
`target_id` for a pet, `target_level`, and the documented target policy. Do not
use this tool to change funding, permissions, identity, or control generation.
The game service owns those decisions. Save the returned handle and poll it
with `game_task_status`; the target is reached only when the receipt and a new
`game_observe` both confirm the requested level. Stop before asking for another
encounter once the target is met or exceeded.

When the profile has a character build, the leveling service spends eligible
points according to that configured policy between confirmed game actions.
It preserves the reserve, sends one point at a time, and waits for server
confirmation before continuing. Do not submit manual `allocate-stat` actions
while this leveling handle is active. The handle also finishes eligible
allocations after reaching the requested level, so a level observation alone
does not replace its confirmed receipt. Unknown points trigger a read-only
refresh; an unresolved old allocation pauses the run instead of being resent.

For sustained leveling, query `game_query_knowledge` with `kind: "rule"` and
`text: "supply"`. Entries identify each configured alias, item template,
unit price and shop interaction point. Compare the shop floor and route facts
with the observed position; an offer being verified does not attest that every
approach is safe. Pass a suitable returned alias (or a reviewed alias already
provided by task configuration) as `parameters.supply_item`.
Optional `supply_target_count` defaults to 10
(2..13), and `supply_reorder_count` defaults to 3 (1..target minus 1). These
select an existing shop offer; never invent an alias or replace it with a shop
coordinate or protocol packet. The reorder threshold counts all reviewed
recovery items, including usable battle drops; the purchase target applies to
the selected shop item's template. The service heals using existing items first,
then restocks at the threshold, reserves two bag slots, confirms the purchase,
and navigates back to the selected leveling area. For finite funding, include
the authorized `maximum_spend` and `reserve_gold` in parameters. Supply policy
does not enable unlimited funds. If no reviewed offer is configured, report
that automatic restocking is unavailable rather than claiming indefinite play.

An inventory increase or purchase submission alone does not prove recovery or
target completion. A paused restock can have already moved or purchased items;
do not start a replacement run or send another purchase to bypass that pause.

During a run, observe after meaningful progress and after supply, death,
party, pet, map, or battle changes. `pending`/`running` is not completion.
Copy each observation's `revision` to `game_action.expected_revision` before a
manual typed action so a stale model turn cannot act on a new battle or map.
For a timeout, poll the existing handle before considering any recovery. Never
blindly restart a leveling run or resend a battle/item action after uncertain
delivery. If the handle becomes `unknown`, pause and request server
reconciliation.

Pause when the target identity disappears or changes, the session is not ready,
the control generation is stale, a route is no longer verified, the character
or pet cannot gain experience, supply/recovery assumptions fail, progress stops,
or the player takes control. Use `game_cancel` for an active handle when the
service permits it, then require a confirmed cancellation before reporting the
run stopped.

The client may present battle animation at its configured maximum speed, but
animation speed is not evidence that the server runs faster. Do not claim a
level or elapsed-time result without server state.
