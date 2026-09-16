# NPC contract drafts

## Riding course draft

`riding-2.5.json` uses the NPC registry schema but deliberately keeps every
`verified` flag false. Loading it fails with `ErrNPCUnverified`. It is not
wired into the packaged gameplay catalogs. Source review is not runtime QA.

| Alias | Floor | NPC tile |
| --- | --- | --- |
| riderman | 1040 | 61,45 |
| riderman-marinas | 2030 | 55,16 |
| riderman-jaja | 3030 | 50,21 |
| riderman-karutana | 4030 | 16,35 |

Canonical approach tiles are respectively (60,45), (55,17), (50,22), and
(16,36). These are convenient positions, not unique permitted positions.
`npcutil.c:398–421` checks the player’s facing direction: at interaction time
the player must face the NPC from an adjacent tile.

All four use the visible name 骑乘训练师. Actor IDs are resolved from the
observed NPC rather than saved across server restarts. Talk range is one.

Menu sequence: 101 (type 2, SELECT data `4`) → 200 (type 2, SELECT data
`1`) → 210 (type 0, YES bit `4`, maximum cost 5000). The SELECT packet
button is OK (`1`); the native handler chooses the entry using `data`.
The first menu has a blank selection entry, so its course option is `4`.
The YES branch sets `CHAR_LEARNRIDE=40`; success is the observed skill
value, not the existence of a success dialog. This does not mount a pet.

Source references: `riderman.conf` lines 5–27, 29–51, 54–68;
`npc_riderman.c` lines 116–174, 190–248; `riderman.create` lines 3–56.
The NPC family field directs settlement revenue, not player eligibility.

Preserved source SHA-256:

- `npc/npc_riderman.c`: `a239d0c4d08167e13e566e656807871b795cfd6842bd4f069d20572a3601fe98`
- `data/npc/family/riderman.conf`: `53f95d90879427e4f9f6fd835cb650705a33ab96c1442858decb43eedb9d8ac9`
- `data/npc/family/riderman.create`: `eb1ac54101a6566970f63a16767feaeb0a90f1b4121035b33cd30b99fba2ab96`
- `include/char.h`: `7d7c6673e614559c577d860e995d9ceef835b5f2c9ad10168b1c4f0b6a6917a8`

Before promotion: verify effective data/build and guide applicability, safe
routes and preparation per destination, each menu/actor identity, paid and
unlimited-funds receipts, already-learned handling, and authoritative learned
skill updates. The existing task targets only the 1040 trainer; the other
aliases are separate destination contracts, not automatic route variants.

The separate `FAMILY_RidePet` handler (`char/family.c:2253–2326`) checks
battle/trade state, selected pet validity, default battle-pet selection and
existing riding state. It requires `LEARNRIDE >= pet level`,
`CHAR_WORKFIXAI >= 100`, and, under the preserved enabled `_RIDELEVEL`
branch, `character level + getRideLevel() >= pet level`. Do not hardcode
5: that belongs to the other compile branch. The enabled `_PET_2TRANS`
branch rejects pet transmigration above 2. Later handler branches also
resolve character/pet riding graphics and family-related rules; this is not
a complete mounting contract. These checks are not course-purchase
prerequisites. Effective build/configuration and visible status mapping
remain to be verified before implementing automatic mounting.

## Adult ceremony draft

`adult-ceremony-2.5.json` has three separate identities: guard 10202 (31,13),
judge 10204 (14,6), messenger 10204 (188,13). Approach tiles used by the task
are (30,14), (14,7), (188,14), respectively. Traversability and complete routes
are not yet accepted. All windows use the observed actor's object ID.

The guard uses message window 271 / YES (4), then warps eligible solo players
to 10204 (2,6); `npc_warpman.c:228–340` establishes eligibility and dialog,
and `include/char.h:326` supplies the sequence. The judge's request is
234 / YES, with event 4 and the default 231 message; 231 / OK only closes
the instructions. Both item-grant and exchange acceptance paths use
235 / YES -> 430 / OK under `_NEWEVENT`. See `npc_exchangeman.c:552–604`,
`1290–1359`, `1460–1499`, `1744–1798`, and `include/char.h:288–295,413`.
The final OK triggers the item effects, so dialog presentation alone cannot
complete either exchange.

The task contains source hashes for all five data inputs. Other preserved
handler inputs used in this review:

- `npc/npc_exchangeman.c`: `89fbfab08d473d6dc772cac08d93e9f9f4b100fbe04a211314e7293bcc9d4435`
- `npc/npc_warpman.c`: `a3a83fc34159a49f4c51c58b53f9c9babf1ade9e599b5e4f7164be06cdd6f7e1`
- `include/version.h`: `081d634dd5c9f87ff6ce28952c96a2e110b51158daadd52dad15a32972da7d94`

Before promotion, resolve guide/version applicability, the selected entrance
route and any pass cost, pet/equipment preparation, survival with all backpack
slots reserved, effective modern batch-grant/exchange patches, runtime
window identity, and complete observed receipts. No new live run was made.
