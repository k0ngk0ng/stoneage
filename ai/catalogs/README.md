# Reviewed gameplay catalogs

NPC contracts may specify `talk_text` for a reviewed quest keyword, for
example `"talk_text": "P|月亮"`. Omitting it retains `P|hi`. The task's
`npc.talk` action may omit `command`, use `talk`, or repeat that exact text;
it cannot submit a different answer. The usual NPC identity, location,
facing, revision and runtime verification checks remain in force. A keyword
contract does not verify the complete quest. See
[2.5 automatic quest progress](../../docs/auto-quests-25-progress.md).

`healing-items-2.5.json` enables the `small-meat` alias for item 2344 under
knowledge fingerprint `c99646422c58e7b95ce76c441cc43385351de4d802308c9a4fd28d281eeea97a`.
It is loaded by Admin/Web through `STONEAGE_AI_HEALING_ITEMS` or
`-ai-healing-items`, and is disabled unless explicitly configured.

The source is `runtime/legacy-server/gmsv/data/itemset.txt`: small meat is
`ITEM_DISH`, usable in the world, targets self or others, and invokes
`ITEM_useRecovery` with base HP 20. Source SHA-256:
`c997393a4806f01833de102939830fc0000bb2a9d445eb214b8501c2b28c4f5d`.
The loader independently verifies this digest against the effective item table;
the general knowledge fingerprint alone does not cover itemset.txt.
Base HP is descriptive: native recovery varies and may be capped by MaxHP.
Execution requires observed consumption and HP increase.

The isolated QA test `TestLivePortableHealingFreshAI` bought two meats from
floor 1004, escaped a normal encounter, consumed one meat, and verified HP
19/35 -> 35/35 and inventory persistence after relogin. See
`docs/ai-quest-sources.md` for the live evidence and its limits. A catalog
entry is a reviewed item contract, not proof that an entire quest is complete.

`shops-2.5.json` and `stock-items-2.5.json` describe the reviewed Samgil meat
shop (floor 1004, NPC 17,13; interaction 17,15). The first native shop item is
2344, priced at 12 stone. The shop's `ItemList:2344-2347` and `buy_rate:1.0`
come from `npc/genout/ss_1004_17_13`; SHA-256:
`faa73290dc05989e1564f7115a4cb843f2959f99ae8f866adb880511a4343283`.
The NPC location comes from `npc/genout/shop_m.create`, SHA-256:
`1b168508524ca1ad012a65169aad7d19a9f6e4fc28201237972c17b4524564c9`.
The general knowledge fingerprint covers the NPC inputs, while the healing
catalog separately binds itemset.txt.

For the packaged control-plane image, enable all three catalogs together:

```sh
STONEAGE_AI_NPC_REGISTRY=/opt/stoneage/ai/catalogs/shops-2.5.json
STONEAGE_AI_HEALING_ITEMS=/opt/stoneage/ai/catalogs/healing-items-2.5.json
STONEAGE_AI_STOCK_ITEMS=/opt/stoneage/ai/catalogs/stock-items-2.5.json
```

These optional startup settings install the reviewed skills. They do not
by themselves create a task or select a stocking quantity. `item.stock`
requests specify an offer alias, a target inventory count, and optional
`reserve_slots` (0 through 15 minus the target count). Reserved slots are
checked from authoritative inventory before travel and immediately before
purchase. A task can pair this with `backpack_free_slots` in its success
conditions so sufficient meat alone cannot satisfy preparation in a full bag.

The gift-exchange task now declares an initial target of five meats, two free
slots, a maximum cost of 60 stone, and a 600-second preparation timeout.
It remains unverified for the full journey. The changed embedded task changes
the knowledge fingerprint; these catalogs were rebound to the newly loaded
snapshot while the reviewed shop and item source hashes above remain unchanged.

The riding source clarification rebinds the catalogs to
`bc7637ebaeef0b68c7623f53181ae03450713a0e60df3b27dadf067eddd81b98`.
The full before/after input manifest differs only at `tasks/riding-basic.json`;
reviewed NPC and item sources are unchanged. Historical QA above remains
historical evidence, not a fresh live run. `drafts/riding-2.5.json` is a
disabled review artifact and must not replace the active shop catalog.

Adding the disabled adult-ceremony declaration rebinds the catalogs to
`732242dadf14674ad2dfdfcada6224a86edbde4973a6a9c7df3989fd79f5fbba`.
Only `tasks/adult-ceremony.json` was added to the full knowledge manifest;
NPC/item inputs remain unchanged. Both riding and adult drafts remain
disabled; no live acceptance was performed during this rebinding.

The shared loader now selects the effective `groupfile` from the sibling
`setup.cf`, just as the wiki previously did. The current snapshot is
`5e9c9d8e8608e32253aa822aae0795748e27e32c3c4ce24fc0d59b66254ea6e6`.
The before/after manifest comparison confirms that 2,140 input files are
unchanged; only `group.txt` (SHA-256
`b1947ee3cad62d9f67d7aa0a4af0258c6d7fd35365883fc0fca0b9ec7ffee926`)
is replaced by `group1.txt` (SHA-256
`0bb651d20c4d8a89db7467a98722c337f51c235cfa853ab27a4be61dcc861b29`).
Evidence: `build/quests-20261003/group-manifest.json`. NPC, item and task
sources are unchanged; the two task drafts remain disabled. The setup file
itself is neither exported nor fingerprinted, since it can hold credentials.

The four disabled axe quest stages bring the current snapshot to
`705a5f29a0b98eca6d47aa180160ab4654954a4c76edf785b8b00316e496ffe3`.
`build/quests-20261003/axe-manifest-diff.json` records the four added task
files and 2,141 unchanged inputs. The NPC/item sources have not changed.
`drafts/axe-2.5.json` is not enabled: the route crosses Ganzo's battle gate,
and real end-to-end acceptance is still pending.

`npc.dialogue` uses the same arguments as `npc.window`, but advances reviewed
MESSAGE pages before choosing the final OK/YES button. The window contract
must contain a free NEXT choice keyed by `32`. It does not retry unknown
writes, authorize arbitrary window types, or infer completion from a click.

A reviewed NPC may declare `passage` with `window_sequence`, `choice`,
`max_turns`, `minimum_level`, and `minimum_hp_percent`. Movement first tries
to detour. An unavoidable matching NPCEnemy may be challenged through a
free, actor-bound MESSAGE/YES contract; the shared PvE executor finishes
the battle and requires the same guard's observed graphic to become zero
before continuing. It rejects party/trade/Arena conflicts and rechecks
requirements at the confirmation boundary. The axe draft contains this
contract, but stays disabled pending a complete normal-mode acceptance run.

Safe travel consumes native `encounter_policy` only when its supported
policy and loaded encounter-table checksum match. Exemptions refer to row
ordinals because encounter IDs can repeat. The server computes them from
its actual loaded groups; missing local records alone never authorize
travel. See the task progress document for native-table comparison and
route-preflight evidence; neither enables an unverified quest definition.

An ExChangeMan YES choice can carry a `pet_delivery` contract: explicit
counted native `predicates`, exact final `accept_text`, `required_items`
and optional `forbidden_items` that exclude other branches. The action must
include the exact authorized stable `pet_ids`. The shared NPC executor refreshes
own state, predicts native ascending-slot deletion, and checks that exact set
at the final write fence. Unknown species/event metadata, replaced instances,
an earlier unauthorized matching pet, or a different message prevents WN.
Uncounted DelPet, overlapping predicates, Pet_Name and EVDEL require separate
source review; do not translate them to a counted rule. This primitive does
not yet supply capture, task-acquired identity persistence or a complete
commission workflow. No village commission has been enabled by this change.

## Four-pet commission catalog (local source, not released)

`quests-2.5.json` includes both existing meat shops and the reviewed voucher
seller/commission manager for `marinas-pet-commission-a` (voucher 20031).
Pair it with `quest-stock-items-2.5.json` and `healing-items-2.5.json`:

```sh
STONEAGE_AI_NPC_REGISTRY=/opt/stoneage/ai/catalogs/quests-2.5.json
STONEAGE_AI_HEALING_ITEMS=/opt/stoneage/ai/catalogs/healing-items-2.5.json
STONEAGE_AI_STOCK_ITEMS=/opt/stoneage/ai/catalogs/quest-stock-items-2.5.json
```

These still require the knowledge/map data directories and automation/receipt
databases. An entirely unconfigured runtime remains disabled. This catalog
requires the matching native build (capture observation, actual gold capacity
and counted event-item deletion); it is not an instruction to enable it on an
older production server.

The v2 stock format introduces `materials` (alias, template_id, verified) and
`itemset_sha256`. The loader checks the effective item table, including exact
identity and duplicate templates, and refuses to use the v2 file without it.
A voucher is purchasable but never enters the healing catalog. Existing v1
healing-only stock catalogs remain supported.

The task buys one voucher (50) and up to eight small meats (96), protects all
original pet identities, collects four new species, and confirms the exact
500-stone reward, voucher decrement and pet removals. Preview requires four
free pet slots, nine backpack slots, 146 stone, room for the reward, no original
pet of a delivery species and no competing local vouchers. The native NPC
selects by slot order, so original same-species pets must first be stored by
the player. No existing pet is automatically discarded or deposited.

Knowledge inputs were compared before rebinding catalog fingerprints; the only
addition was `tasks/marinas-pet-commission-a.json`. Existing unverified quests
and draft NPC catalogs keep their review status. Native HTTP acceptance and
remaining scope are tracked in [the progress document](../../docs/auto-quests-25-progress.md).

The other three village A pet commissions are draft-only (Samgil/Karutana level 2, Jaja level 5). Their shop approaches must be reachable from the actual entrance, not merely walkable. Native source contracts and per-species safe routes are checked; natural preparation and full HTTP collection/delivery acceptance remain incomplete. Do not promote execution flags from these static checks.
