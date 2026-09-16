---
name: stoneage-play
version: 1.2.0
description: Play StoneAge through the bound game MCP session when an AI player must observe the world, choose a verified objective, and take legal typed actions.
---

# StoneAge play

Use the bound character session as the only source of game state. Start every
decision with `game_observe`; the response is authoritative for position,
phase, battle readiness, party, pets, inventory, and connection state. The
session is already bound to one character and control generation. Never ask
for or provide an account, password, character identifier, gateway address,
raw packet, shell command, or arbitrary URL.

Before choosing a task, route, leveling area, NPC, or cost, call
`game_query_knowledge`. Query the relevant `task`, `leveling`, `route`, or
`rule` entry and use only entries marked `verified`. If the knowledge response
does not establish a route or prerequisite, report the missing fact and pause;
do not invent a StoneAge 2.5 coordinate or攻略.

Use `game_action` only for the closed typed action vocabulary exposed by the
tool. Copy the latest `revision` from `game_observe` into
`expected_revision` for every action. Check that observation before movement, dialogue, window selection,
item use, pet changes, party operations, chat, or duel. Supply IDs observed in
the current session and let the game service validate visibility, phase,
ownership, and control generation. Do not repeat a non-idempotent action after
a timeout until its server result is reconciled.

Actions and long-running skills return receipts. A `pending` or `running`
receipt is a handle, not success: poll it with `game_task_status({"handle": "<receipt handle>"})` and accept a
goal only after a `confirmed` receipt contains server evidence. For an
`unknown` receipt, use the action-specific read-only reconciliation below when
available; otherwise preserve the handle and pause the affected operation.
The communication cases below do not require stopping unrelated activities. Use `game_cancel` when the
player takes control, the goal becomes invalid, or an operation exceeds its
allowed wait; a cancellation itself also requires a confirmed receipt.

Pause game-changing actions when the connection is not ready, control has been taken
back, knowledge is unverified, the same observation makes
no progress, or a safety boundary is reached. Preserve the handle and last
observation in the explanation so a later turn can resume without resending an
action.

For character attribute points, use only the configured build. When
`flags["build:allocation_allowed"]` is true, `own_progress["build_next_stat"]`
is the service-selected native index (0=vital, 1=strength, 2=toughness,
3=dexterity). Between tasks, submit one `allocate-stat` action with that index
and the observed revision, without value or command. Weights balance existing
base attributes; zero weights receive no points and the configured reserve
stays unspent. Without an allowed index, leave the points alone; personality,
quest advice and pet targets do not authorize a different build.

For this allocation's unknown receipt, read `game_observe` and query the same
handle with `game_task_status` to reconcile it. Only a `confirmed` receipt
proves the service observed one point spent and the selected attribute rising
by one. Do not submit another allocation while that receipt is unknown. If it
cannot be confirmed, preserve the handle and pause; do not infer success from
packet submission or replay it after reconnecting. Observe again before
choosing the next point.

For `mail` with `command: "list"`, query the same receipt handle with
`game_task_status`. Confirmation requires a new complete address-book response
after the query; a previously cached list or incremental update is insufficient.
Do not issue another query merely to poll the first one.

Public `chat` and ordinary `mail` with `command: "send"` have no reliable
sender acknowledgement. An `unknown` receipt means delivery is unproven: keep
its handle and message in private notes, do not resend that message or record
it as delivered/read, and do not poll indefinitely for an acknowledgement that
the protocol does not supply. Continue life heartbeats, observation, memory,
reminders and unrelated activities that do not depend on its delivery. A new
natural conversation may justify a different message; silence alone does not.
This exception does not authorize continuing an uncertain trade, item use,
stat allocation or another operation whose effects are required by the next step.
