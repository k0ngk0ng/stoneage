---
name: stoneage-social
version: 1.2.0
description: Interact with StoneAge players through bounded chat, game mail, party, trade, and duel actions while preserving consent, current visibility, and the bound character identity.
---

# StoneAge social play

Call `game_observe` before addressing another player. Use only actor IDs and
party members visible in that observation. For game mail, use the observed
address-book slot instead; recipients need not be currently visible. Slots can
be reused and are not permanent player identities. Use `game_query_knowledge` with `kind: "rule"` when a
party, trade, duel, or location rule is uncertain. The knowledge service is the
authority for this server, and an absent or unverified rule requires a pause.

Use `game_action` with its typed `chat`, `mail`, `party`, `duel`, `social-setting`, or
`trade` form and copy the
latest observation `revision` into `expected_revision`. Keep chat
short, truthful about what the agent knows, and clear that the speaker is an AI
player when the product policy requires that disclosure. Do not put a password,
secret, raw packet, URL, arbitrary command, or another role's identifier in
chat or action fields. A party invitation, trade, or duel request is not proof
of acceptance; observe the resulting party/battle state before acting on it.

For a multi-step interaction, state the intent, submit one legal action, then
observe and confirm the response before continuing. Do not repeatedly invite,
challenge, or message a player when no state progress is visible. Respect a
decline, timeout, departure, or changed target and stop the interaction.

Pause for an invisible or changed actor, ambiguous acceptance, unsupported
trade/duel rules, stale control generation, connection loss, an unexpected
battle, or a human takeover. Do not infer hostility from silence or a failed
request. If an outstanding social operation returns a handle, poll it through
`game_task_status({"handle": "<receipt handle>"})`; `unknown` requires reconciliation of the affected operation,
and cancellation must be confirmed before considering an ongoing interaction ended.

Ordinary chat and mail-send lack reliable sender acknowledgements. Preserve
an unknown message and its handle, never resend it to seek confirmation, and
do not claim it was received or read. Do not wait or poll forever: continue
heartbeats and unrelated life activities. A later incoming message or a new
natural topic can justify another distinct message; lack of a response does not.
This does not relax the confirmation needed for trades, invitations or duels.

For mail, use `kind: "mail", command: "list"`, then poll that query's handle
and observe the resulting `address_book`. `command: "add"` exchanges name
cards with the player in front of the character using its current x/y;
observe the resulting contact before sending. `command: "send"` takes the
observed contact `index`, `text` and optional `color`. Incoming `msg`/`pmsg`
chat entries are mail; their `from_id` is a transient address-book index,
not a public-chat actor ID or proof of persistent identity.

Social preferences come from `flags["social:known"]` and the `social:party`,
`social:duel`, `social:party-chat`, `social:trade-card`, `social:trade` flags.
To change one setting, use `kind: "social-setting"`, its setting name in
`command`, and `value: 0` or `1`. Wait for the observed flag before changing
another setting; the runtime preserves unrelated preferences. Enable trade
or duel only when it fits the player's current goal and stated intentions.

For a trade, face the intended visible player and use `command: "request"`
with that actor's `target_id`. The server chooses and returns the counterpart;
verify the returned identity before offering anything. Use `offer-item` with
`index` 0 or 1 (offer position) and `value` 5 through 19 (backpack slot),
`offer-gold` with the offer position and positive amount, or `offer-pet` with
`pet_slot`. There are two item/gold offer positions and one pet position.
Changes replace that offer position. Check both offers after each operation.
Unlimited NPC funding does not waive the character's external-transfer limit.

`lock` submits your offer lock. After the peer has also locked, inspect the
exact item slots, amounts and pet offered, then use `confirm` once. The runtime
builds the native confirmation from those offers; do not supply packet text,
peer connection numbers or names in action parameters. A submitted own offer
or lock is not a server acknowledgement. If the peer changes an offer after
locking, the result is uncertain, or a human has edited the trade, cancel or
pause instead of confirming. Use `cancel` and await the server's closed state.
A closed trade window does not distinguish completion from cancellation:
verify inventory, owned pets and gold before recording a completed exchange.
