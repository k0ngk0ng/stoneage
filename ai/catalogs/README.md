# Reviewed gameplay catalogs

`healing-items-2.5.json` enables the `small-meat` alias for item 2344 under
knowledge fingerprint `4244c8871551d59232ad249299d3998bf38b2791afc5a74d4e2218e018458177`.
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
