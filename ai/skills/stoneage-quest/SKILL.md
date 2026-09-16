---
name: stoneage-quest
version: 1.2.0
description: Complete a verified StoneAge quest or quest chain for the bound character with checkpoints, authoritative completion checks, and safe recovery.
---

# StoneAge quests

Use `game_query_knowledge` with `kind: "task"` before starting. Query the
specific task ID and then query `facts` or `rule` entries when a prerequisite,
NPC choice, item, cost, or completion flag is unclear. Treat the knowledge
revision and each `verified` field as part of the plan. A missing or
unverified branch is a pause condition; never fill it with a remembered
guide, guessed coordinates, or a model-generated route.

Task `dependencies` contains prerequisite task IDs. Query each recursively
before committing to a chain, keeping shared prerequisites only once and
stopping on missing, cyclic or unverified definitions. Each task retains its
own quote, budget and handle: the requested task's budget does not include its
prerequisites. Check the aggregate expected/maximum cost against the user's
authorized chain budget before starting; never assume an individual-task
quote covers the chain. Reuse an existing handle for an unfinished prerequisite.

Compare each prerequisite's machine-readable `success` conditions with a fresh
observation. If already satisfied, skip its execution; otherwise complete its
own prerequisites and run it through `game_start_task`. After confirmation,
observe again before starting its dependant. Old receipts and memory alone do
not satisfy an entry gate. The runtime validates the full dependency graph and
adds direct prerequisite completion conditions to the dependant's preflight;
it does not silently run prerequisite actions. Do not require an ancestor's
reward item to remain after an intermediate task legitimately consumed it.
Prerequisite completion must be observable again after reconnecting;
`window_submitted` alone is a local submission marker and is rejected in a
dependency's completion contract. Obtain a reviewed server-state predicate.

Read `preparation_reviewed`, `preparation_notes` and the character/pet level
conditions before starting any node. Unreviewed preparation or an unknown pet
requirement is a reason to stop and obtain the missing guide/version evidence,
not to learn the route by repeatedly dying. Meet the reviewed levels first.

A `pet_level` condition whose ID is `$selected_pet` needs an explicit owned pet
binding. Choose its stable `id` from fresh `game_observe` pets and pass it as
`parameters.selected_pet_id` to `game_start_task`. Use the same pet for the
chain and any preparatory leveling; interpret the placeholder as that pet when
checking conditions. Never substitute its name or slot, lower the reviewed
level, or silently select a replacement after it disappears. Running handles
retain their original pet identity across resume.

Call `game_observe` before planning and after every dialogue, window choice,
item use, battle, map transition, and delivery. Confirm the expected change in
the server-observed state (inventory, flag, location, window, or receipt) before
advancing. A model reply or a chat line is not proof that a quest step worked.

When a manual recovery action is needed, copy the latest observation `revision`
into `game_action.expected_revision`. When the knowledge entry is executable, call `game_start_task` with its `task_id`
and only the documented `parameters`. Keep parameters small and do not put
credentials, raw protocol data, URLs, or a different role in them. Record the
returned handle. Poll it using `game_task_status`; `pending` and `running` mean
wait, `confirmed` means inspect the evidence and then observe again, and
`failed`, `cancelled`, or `unknown` requires recovery or a pause.

Recover by observing the current state and matching it to the last verified
checkpoint. Do not restart a task or repeat a purchase/delivery merely because
the request timed out. If the server state cannot distinguish whether a
non-idempotent step arrived, pause for reconciliation. If a human takes over,
stop issuing actions, cancel the outstanding handle when permitted, and leave
the quest at a resumable checkpoint.

Pause for missing prerequisites, an unexpected NPC branch, a full inventory,
loss of connection, stale control generation, unverified cost, no progress,
or any request to spend outside the configured limit. Report the exact missing
knowledge or server evidence needed to continue.
