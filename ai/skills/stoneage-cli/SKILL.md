---
name: stoneage-cli
version: 1.0.0
description: Play StoneAge through the sactl command-line client. Use when the character must look at the world, move, talk or wait for events.
---

# Play through sactl

`sactl` is a real game client that keeps one character logged in. You drive it
with ordinary shell commands. Every command prints the truth the server just
told the client — there is no task handle, no receipt and no polling loop.

```bash
sactl status            # session + position + nearby + inventory, one screen
sactl observe           # the same observation in full
sactl --help            # the command list
sactl walk up           # one step: up/down/left/right or n/ne/e/se/s/sw/w/nw
sactl goto 18 22        # walk a route to a tile on this floor
sactl exits             # the warps that start on this floor
sactl warp 1000         # take the exit to that floor (a multi-hop route is planned)
sactl say "hello"       # public chat (CP936, 68 bytes maximum)
sactl talk 村长 "你好"   # face a nearby character and speak to it
sactl choose 2          # answer the open window by the row number it showed
sactl reply ok          # answer a message window: ok/cancel/yes/no/prev/next
sactl battle "H|FF"     # one battle turn: H attack, T defend, S|NN|FF skill, N wait
sactl item use 5        # inventory and field items: use/drop/move/pickup/magic
sactl mail list         # the address book; mail add / mail send <slot> <text>
sactl query c           # ask the server for one status stream
sactl functions         # the raw escape hatch: every remaining client function
sactl log 20            # the last server events
sactl wait 30s          # block until the server sends something
```

Talking to an NPC: walk next to it (one tile, any direction), then
`sactl talk <name>`. The reply may be chat, a window, or nothing at all — a
2.5 NPC without a dialogue handler legitimately stays silent. When a window
opens, `observe` shows its message and its numbered choices; answer with
`sactl choose <row>` using the number shown. The numbering is the server's
own, so use it exactly as printed.

Rules that matter:

- Look before you act. `sactl status` is cheap; run it whenever you are unsure.
- `walk` and `goto` already wait for the server to confirm the new tile, and
  they report `blocked` when the tile cannot be entered. A blocked step is
  information, not a failure to retry blindly.
- `sactl goto` refuses tiles that are not walkable, so use it instead of
  guessing directions.
- The observation lists other visible entities with their tile coordinates;
  use those coordinates to decide where to walk.
- Chat is public. Say something only when it adds to the scene; silence is a
  normal choice.
- If a command fails, read its message. The client reports what the server
  said instead of hiding it.
- Do not describe an action as done unless the command's own output confirms
  it. If a command refuses or the server rejects it, say so instead of
  claiming it happened.
- Indoor maps have no exits except through `warp`: `sactl exits` lists the
  tiles that leave this floor, and `sactl warp <floor>` walks to one and uses
  it. Warps are side effects — never repeat a warp whose result is unknown;
  observe first.
- Some things are only reachable through `sactl send <FUNC> ...`. Check
  `sactl functions` for the exact argument list before using it.
- Battle: battles only start where the encounter table allows one — use
  `sactl encounters` to see the areas on this floor and `sactl seek-encounter`
  to walk into one. Each turn needs two submissions: yours
  (`sactl battle "H|FF"` to attack) and your pet's (`sactl battle "W|FF|FF"`).
  The turn only resolves after both are in, and the player's command must be
  submitted first — the pet's is rejected before it. While the server
  animates the round, `command_ready=false`; when it finishes,
  `sactl battle-end` (EO) advances the turn or leaves the battle.

Start every decision with an observation, then take one step, then look again.
