# StoneAge 2.5 quest sources

This index records sources inspected on 2026-09-15. It is a research index,
not a verified executable quest catalog. Runtime task coverage is reported by
`internal/aiknowledge`; the embedded riding and gift-exchange tasks remain
unverified.

## Preparation review before live journeys (2026-09-16)

Read the specific walkthrough before attempting a journey. The NPC's minimum
acceptance level is not evidence of survivability along the route. Record
server eligibility separately from the AI's enforced departure minimum. Review
character and required pet levels, attributes, equipment, healing, inventory
space, prior tasks, encounters and any party requirement. Select an appropriate
verified leveling area for the missing levels; do not repeatedly attempt the
quest at the acceptance minimum. A guide's recommendation becomes an AI
departure constraint only after mapping it to this server and this branch.

The following complete pages were read, not just their search snippets:

| Guide | Actual finding | Decision for this server |
| --- | --- | --- |
| [17173 送贝壳的故事](https://news.17173.com/z/stoneage/renwu/b3.htm) | 弥生 gives a shell; 日美子 exchanges it for a flower. Warns against dropping the shell. No character or pet minimum is stated. | The current savepoint-0 task runs the opposite flower-to-shell branch. Do not copy this guide's route or invent a level threshold from the word “simple”. Full journey preparation remains unreviewed. |
| [17173 神奇的贝壳](https://news.17173.com/z/stoneage/renwu/n9.htm) | A different quest: 卡鲁它那 merchant, 哥亚山 cave, two shells and a level-1 pet reward, followed by a letter/flower errand. | This is not evidence for hometown gift exchange. The reward pet's level is not a required pet level. |
| [17173 成人仪式](https://news.17173.com/z/stoneage/renwu/n10.htm) | Says level 30 permits entry, level 35 is easier; mentions roughly level-30 strong encounters, a 200-stone pass on one route and 15 ritual items. | Candidate eligibility 30 and departure recommendation 35 must remain distinct. Effective NPC rules, route, item capacity and pet requirement still need mapping before adding an executable task. |
| [2.5 精灵少女篇](https://www.shiqi.club/shiqi1063.html) | Requires completed adult ceremony; recommends level 115; describes dangerous frog encounters and version-specific rewards. | Recommendation is not a verified server gate. No numerical pet minimum was established. The task chain remains research-only. |

Local reading copies: `build/ai/quest-sources/17173-{gift,shell,adult}.txt`
and `spirit-girl-25.txt`. The first three historical pages do not themselves
claim a specific 2.5 build. Their version applicability needs server mapping.

Task definitions now expose `preparation_reviewed` and `preparation_notes`.
These are maintainer assertions, not model parameters or automatic proof.
Record source links, applicable branch, server differences, numerical departure
requirements and their rationale in the review notes. Required character/pet
levels must also appear as machine `character_level` / `pet_level` preconditions;
the latter binds a specific stable pet identity in the executable plan. Do not
treat an unreviewed pet requirement as “no pet required”. Tasks without a
review and notes cannot compile or be advertised as verified. Both existing
embedded tasks still lack this review; no guessed thresholds were added.

The full gift-journey live entry point checks preparation before creating a QA
character or accessing containers. Existing pickup and protocol evidence remains
valid within its original scope; it is not full-journey readiness evidence.
Enabling a live-test environment flag does not bypass this preparation check.

Shared deterministic task travel now applies the leveling navigator's encounter
policy to the entire proposed ground route. Positive-probability encounter rows
must resolve to verified enemy data within the observed character's permitted
level range; an unsafe shortest path triggers collision-aware detour search.
Cross-map search excludes unsafe arrival tiles, and ground travel avoids warp
source tiles whose destinations violate that policy. Each submitted segment
and warp destination is checked again against fresh character observations.
This adds executable detours for human and AI tasks, not a source-derived quest
level recommendation or proof that a character can survive the quest. Shared
travel now also checks observed character, riding-pet and selected battle-pet
health before encounter movement/warp arrival, with reviewed single-item pet
recovery and a fresh state check before continuing. Dead/missing pets, unknown
riding state and uncertain recovery stop travel. This runtime recovery does not
replace review of required pet levels, equipment, consumable capacity or paid/NPC
transitions. No task verification flag changes with this work.

| Source | Inspected content | Applicability and limitations |
| --- | --- | --- |
| [17173 historical quest index](https://news.17173.com/z/stoneage/renwu/renwu.htm) | Separate 2.5 section with spirit-girl installments and dark spirit king guides; 2.0–2.5 references, island quests, rebirth caves, JOT and SOT routes. | Use the explicit version sections. The same page also lists 3.0–8.0 content. Reading an index does not verify each linked walkthrough or establish that all tasks exist on this server. |
| [Spirit-girl 2.5 walkthrough](https://www.shiqi.club/shiqi1063.html) | Community article dated 2017-06-09; adult-ceremony prerequisite, NPC and cave coordinates, item exchanges, spirit-king encounters and final reward discussion. | Supplemental historical evidence. It explicitly distinguishes Taiwan and mainland rewards and includes unproven speculation. Its recommended level 115 is advice, not a verified server eligibility requirement. |
| [17173 StoneAge portal](https://stoneage.17173.com/) | Historical version navigation explicitly includes 2.5, 精灵王传说. | Discovery source only; general portal content spans later versions. |

Readable research copies are kept locally under `build/ai/quest-sources/`
(`17173-quests.txt`, `spirit-girl-25.txt`, `17173-alt.txt`). Full walkthroughs
are not reproduced here. These copies contain historical spelling and text
decoding artifacts; uncertain names or coordinates need independent checking.

## Turning a guide into an executable task

For each task, record its guide URL and claimed version, then map the steps
to the effective server's NPC creation records, configuration, scripts and
compiled behavior. The preserved `server/legacy/source/2.5/gmsv` tree is a
reference; the running server's mounted data and enabled patches determine
what the AI can actually do.

The execution contract must specify prerequisites, dependencies, NPC identity,
player interaction position, window sequences and choices, item requirements,
costs, branch conditions and observable completion predicates. In particular,
`move` targets the player's destination; `npc.talk` coordinates identify the
NPC actor. They can differ by one tile.

Keep three distinct evidence stages:

1. **Source collected:** the walkthrough is accessible and its version is
   identified; its steps are still unverified.
2. **Server mapping checked:** source hashes and effective rules support the
   contract, including any differences from the historical walkthrough.
3. **Execution verified:** an isolated QA character completed the contract,
   with authoritative inventory/event/skill observations and recovery checks.

Only the final stage, together with matching source hashes, permits an
executable task to be marked verified. A successful model response, a packet
write, a guide index entry or static NPC file count is insufficient.

When a guide conflicts with the effective server, document the difference
and follow the server rule. Do not infer missing completion flags or dialog
choices from the language model's memory. Keep missing branches visible in
coverage reports so an incomplete quest chain cannot be reported complete.

## Current task coverage

### Basic riding course: preserved-source eligibility review

The preserved `npc/npc_riderman.c` handler and
`data/npc/family/riderman.{conf,create}` were read together. The initial talk
requires the player to face the NPC from an adjacent tile (lines 116–139;
`npcutil.c` lines 398–421); window submissions
require distance at most one (lines 142–174). The basic-course branch
(lines 190–227) rejects an already learned `CHAR_LEARNRIDE >= 40`, charges the
configured 5000 stone, sets that field to 40 and sends the gold and learned-ride
status updates. The “Lv40” course text describes mount eligibility, not a
minimum character or pet level for buying the lesson. This handler establishes
no numerical character/pet learning minimum.

The NPC's `family` field selects the settlement's revenue destination
(lines 229–248); this branch does not compare player family membership. The
embedded task now explicitly avoids treating player family membership as
a prerequisite. Its coordinates select only the
1040 trainer, while the create file also contains trainers at 2030 (55,16),
3030 (50,21) and 4030 (16,35). These are separate travel destinations, not
interchangeable aliases at runtime.

File hashes and source-review scope are recorded in
`build/ai/quest-sources/riding-course-source-review.json`. This review does not
establish safe routes from arbitrary starting locations, guide applicability,
effective deployed behavior or a complete paid-course receipt. The task keeps
its existing preparation/execution blockers; no character or pet threshold
was invented from the course title and no verification flag was raised.

### Adult ceremony: source mapping (2026-09-16)

The local runtime `map/mapwarp.txt` also contains a free northern entry chain
from hometown 0: floor 1006 → 1000 → 100 → 1100 → 10201 → 10202
(lines 1405, 1320, 1350, 2553, 2555). The island entry at (470,631)
corresponds to the guide's northern 柯奥村 approach. Riding trainer floor 1040
has a shorter free chain 1006 → 1000 → 100 → 1040 (last edge line 427).
These are **floor-graph connections only**: they do not prove walkable ground
paths, encounter preparation or arbitrary-start reachability. The guide's
200-stone southern pass must not be added as a universal requirement when
reviewing the northern route. Positive-probability areas with missing group
data, including hometown 0's known gap, remain unreviewed travel territory.

The full 17173 adult-ceremony page above was compared with the preserved 2.5
data and the local QA data directory. All six referenced data files match
byte-for-byte; hashes are recorded in
`build/ai/quest-sources/adult-server-mapping.json`. This checks files on disk,
not the configuration or behavior of a running server.

| Contract | Source and finding |
| --- | --- |
| Entrance | `npc/genout/event04n.create:7–16` creates the guard at floor 10202, (31,13). `npc/genout/wpm_10202_31_13:1–3` specifies destination 10204 (2,6), `FREE:LV>29`, `MONEY:-1`. Level 30 is the entry minimum; unlimited funding must not replace this eligibility condition. |
| Solo entry | `npc_warpman.c:228` checks `NPC_PARTY_CHAECK`; its definition at lines 560–565 requires `CHAR_PARTY_NONE`. The guide independently says the ceremony cannot be attended in a party. Empty client party rows alone are insufficient proof of this server state. |
| Judge | `npc/jaruga/event/event.create:28–38` places the judge at 10204 (14,6). `event04_1:15–23` starts event 4. Its `LV>0` request condition does not replace the separate entrance requirement. |
| Ritual items | The messenger is at 10204 (188,13), `event.create:40–50`. `event04_2:3–10` requires `NOWEV=4&ITEM!=2417` and grants `2417*15`, explicitly requiring 15 free backpack slots. A generic five-meat stocking step would conflict with this requirement. |
| Completion | `event04_1:3–12` accepts 15 items 2417, deletes them, grants item 2418 and sets end-event 4. Completion must observe the flag and inventory transition, not infer success from a dialog write. |
| Reward difference | `itemset.txt:2000–2001` identifies 2417 as 仪的玉 and 2418 as 仪之兜. The local reward description says defense +15, agility -3, while the guide says agility -2. Use server data for the reward contract. |

The guide's level-35 recommendation is a candidate departure minimum, distinct
from the confirmed level-30 entrance gate. Pet preparation, accessible routes,
encounters, interaction tiles and window choices still require review. No
numerical pet requirement was found; that is not a finding that a pet is
unnecessary. This mapping now has an embedded `adult-ceremony` definition, still excluded
from execution while preparation and execution remain unverified. It establishes the next
implementation requirements: a ceremony-specific inventory plan and reviewed
travel preparation. The read-only observation module now exposes optional
`party_mode` from `CHAR_WORKPARTYMODE`; the headless client invalidates old
evidence on native party updates, party submissions and login/logout. Service
observations expose `party:known` and `party:solo` only in a connected world
state. A task can require `flag_set:party:solo`; older servers and an empty
party display cannot satisfy it. C harness and focused Go race checks passed
in `build/ai/party-observation-{c,go}.log`. Existing QA binaries were not
replaced, and this is component evidence rather than live ceremony acceptance.

The judge's exact item-only ACCEPT contract now has a dedicated modern server
exchange path (`stoneage_adult_exchange.c`, patch `0020-adult-item-exchange`).
It requires all six reviewed action/identity fields and all four known message
fields exactly once; changed scripts or additional effects use
the existing script engine. For this contract, fifteen distinct unstacked
backpack items 2417 are fixed before allocating the detached reward 2418.
Allocation or input revalidation failure consumes nothing. The commit reuses
the first consumed slot, sets native ownership metadata, removes the remaining
ingredients, and only then publishes inventory updates. No sixteenth free slot
is required. Equipment is not counted or modified. Existing callers retain the
completion flag and dialog handling after a successful return.
Preparation failure now also emits an explicit retry-later message. Source
review found that the original `NPC_EventDelItem` quantity branch does nothing
when `breakflg=0`; this script has no `Break` directive. The dedicated path
therefore also fixes the retained-ritual-items behavior, while leaving other
scripts' legacy semantics outside this change.

`test-adult-item-exchange.py` uses the preserved script's actual CP936 bytes,
compiles the module with warnings as errors, and checks full-backpack success,
allocation failure, missing/aliased/stacked inputs, input changes, equipment
exclusion, repeated delivery and unsupported-script fallback. The relevant
existing module/funding patches and new NPC/makefile wiring apply without fuzz.
Evidence: `build/ai/adult-item-exchange-basic.log`. General mixed-effect scripts
and character-save crash recovery remain open alongside travel preparation
and full execution verification.

The messenger's exact `event04_2` contract now uses the same server integration
with a separate batch-grant path. It requires fifteen truly empty backpack
slots and no existing ritual item, including equipment slots. All fifteen
items are created with detached ownership first; failure cleans up every
prepared item before any backpack write or inventory publication. The module
rejects wrong templates or unexpected stack counts and rechecks the reserved
slots before committing. Once all preparations pass, it binds all ownership
and slots, then logs and publishes. Repeating the operation with ritual items
already present cannot grant another batch. The native `NPC_EventFile` condition
check still owns `NOWEV=4`; the messenger contract has no `EndSetFlg` and does
not itself complete the ceremony. Additional script effects use the existing
engine rather than being silently skipped by this dedicated path.

The independent `test-adult-item-grant.py` harness uses the real messenger
script and compiles the actual module with warnings as errors. It checks
failure at each of the fifteen allocation positions, wrong/stacked templates,
insufficient capacity, an equipped ritual item, slots changing during
preparation, repeat requests, native ownership and unsupported extra fields.
The main agent reviewed and reran this harness and the judge-exchange/patch
checks successfully. Logs: `build/ai/adult-item-grant-basic.log` and
`build/ai/adult-item-exchange-basic.log`. These remain component results, not
proof of a complete journey, saved-state recovery or model-driven execution.

| Definition | Evidence available | Still required |
| --- | --- | --- |
| `riding-basic` | Local NPC coordinates and dialog contract; argument validation tests. | Effective-server prerequisites, payment and learned-skill confirmation in live QA. |
| `hometown-0-gift-exchange` | Local NPC scripts, savepoint-0 prerequisite, flower template 2415, shell template 2414 and compiled window constants. Main-agent live QA verified pickup of flower 2415 and `now:2`, with matching effective NPC-script hashes. | Complete travel and exchange, authoritative completion and recovery checks. |

The fresh-character QA has confirmed one village-entry warp and the server's
savepoint and known-empty-backpack observations. Those results support the
testing infrastructure; they do not establish either quest's completion.

The first live gift-pickup attempt stopped during village movement at
`1000 (79,72)` with a stale-revision rejection, before NPC interaction.
It did not verify flower delivery or quest progress. That diagnostic log is
retained as `build/ai/gift-pickup-stale-revision.log`.

After bounded recovery for explicitly unsent stale moves and clearing actors
on a floor change, the main-agent race-enabled rerun passed. The character
reached `1000 (56,124)`, interacted with the visible 日美子 actor at `(57,124)`,
received request window 234 with YES/NO buttons 12, selected YES 4, and received
window 231, flower template 2415 (one occupied slot) and `now:2`. The effective
NPC scripts matched the reviewed sources. Evidence is in
`build/ai/gift-pickup-live-evidence.json` and `build/ai/gift-pickup-live.log`.
The temporary gateway was independently confirmed closed. This verifies the
pickup only; the complete task retains `execution_verified: false`.

The extended live rerun also exercised recovery: the real pickup succeeded,
then the local `submitted` checkpoint write deliberately failed, leaving
`prepared` durably stored. The test closed the game session, reloaded the
profile, logged into the same character and confirmed exactly one flower and
`now:2` from the new session. After reopening the checkpoint database, the
engine reconciled the pickup without another NPC submission (request count
remained one). Log: `build/ai/gift-checkpoint-live.log`. This is injected
checkpoint-write failure plus actual game relogin/database reopen; it is not
an operating-system process-kill or model-container recovery test.

## Full exchange route limitation

Main-agent checks against the actual QA data found that all nine ordinary
mapwarp exits from floor 1000 to floor 100 arrive in effective encounter area
21 (probability 1–5), which references missing enemy group 1230. A level
increase cannot establish correctness of incomplete encounter data. The
bounded route probe produced 248 candidates before its explicit 30-second
timeout; this is not an exhaustive search of safe detours or NPC travel.
The independent arrival-point check is recorded in
`build/ai/gift-exit-risks.json`, with scope and reproduction notes in
`build/ai/gift-route-evidence.md`. The full exchange remains unverified,
pending encounter-data and server robustness work followed by live execution.

The preserved original `vendor/archives/stoneage2.5.tar.gz` was checked
without extraction or modification. Both archived group tables lack group
1230; both archived encounter tables reference it in area 21 with weight
100. This rules out a simple runtime-import omission or replacement from
the archived `group1.txt`. Archive/member hashes and the original area row
are recorded in `build/ai/encounter-data-provenance.json`. No substitute
enemy-group definition has been invented or installed.

The modern server build now applies `0014-safe-enemy-group-bounds.patch` to
reject missing group/enemy references and preserve legitimate empty slots.
Main-agent guard tests, actual C compilation/static linking and normal live
pickup/recovery/relogin smoke passed on the updated isolated QA binary.
This repair does not restore missing group definitions or verify the
affected encounter areas; full exchange remains pending.

The opt-in `TestLiveGiftExchangeFreshAI` now continues the real fresh-character
pickup through ordinary travel toward Yayoi. It checks the running local QA
binary hash before provisioning and compares the effective Yayoi scripts with
a fixed reviewed fingerprint. Its full success predicate requires shell 2414,
removal of flower 2415, `end:2`, and preservation after a second real relogin.
This is a gameplay integration check, not a model-driven task acceptance test.

The first run reached floor 100 `(608,520)` and timed out waiting for a movement
endpoint, without an active battle. The server's `lssproto_EN_recv` clears the
remaining walking queue before attempting to create an encounter, including
when the encounter cannot be created. `MovementSkill` now sends and confirms
one tile at a time when the current or upcoming cells have encounter
probability, retaining four-tile requests elsewhere. Tests model a successful
first step followed by a discarded walking queue, across all three ground
movement paths; they do not retry uncertain writes or mark missing enemy data
verified.

After that change, the second real run continued to floor 100 `(479,524)` and
entered an actual battle. Travel then stopped at the existing battle guard.
Logs: `build/ai/gift-exchange-live.log` and
`build/ai/gift-exchange-live-after-segmentation.log`. Structured partial
evidence: `build/ai/gift-exchange-evidence-174073441.json` and
`build/ai/gift-exchange-evidence-1039491.json`. Service race tests, full repository
tests and service vet passed. The next gap is battle handling and confirmed
travel continuation at that stage; neither Yayoi delivery nor full quest completion was
verified, and `execution_verified` remains false.

The next live run added bounded solo-PvE escape and route continuation. The
headless protocol now distinguishes the native menu packet from a short
`BP|BE|e0|f1|` movie, tracks player and pet command submissions independently,
and accepts only the local player's `BE|e<slot>|f1|` as escape confirmation.
EO requires a terminal server result and is sent once; late BP/BC packets
cannot revive an escaped battle. Movement only replans after an explicitly
observed battle interruption, never after an uncertain W write or timeout.

`build/ai/gift-exchange-live-battle-recovery.log` records eight actual successful
escapes followed by EO and world movement. On the ninth encounter the existing
eight-battle travel limit stopped the run at floor 100 `(265,560)`, HP 1, still
holding flower 2415 with no shell or `end:2`. Structured evidence is
`build/ai/gift-exchange-evidence-3120637279.json`. Limits were not raised to force
a passing result. This proves bounded combat escape and travel continuation;
healing/resupply and route risk handling remain necessary before claiming
complete exchange or sustainable model-driven gameplay.

The common movement write boundary now requires at least half of observed
maximum HP (rounded up) when the current tile or any requested step overlaps
an encounter area. Unknown maximum HP also stops that segment. This covers
same-floor movement, ground segments before/after warps, and the fresh-state
check before a rejected stale-revision write is retried. Missing enemy groups
do not exempt an encounter area. Non-encounter town movement remains available
for reaching a healer. This is a minimum health check, not an enemy-level risk
assessment or automatic healing; a low-HP character already in the wilderness
still needs an explicit recovery solution.
Independent review also identified that a warp destination can enter an
encounter area without a W segment on that floor. Cross-map movement now
checks the destination health requirement before approaching or activating
the warp, including when already standing at its source.

`travel_health_test.go` verifies that one-HP escape recovery sends no next W,
that declining HP prevents the safe pre-write retry, and that town movement
remains available. Service tests and focused race tests passed, recorded in
`build/ai/travel-health-tests.log` and `build/ai/travel-health-race.log`.
No additional live gift journey was run for this change.

Healer source review confirms that `npc_windowhealer.c` parameters describe
the charge threshold level, HP rate, MP rate and interaction range. Normal
HP/MP prices derive from character level times the corresponding rate (with
integer conversion and minimum-charge rules); combined healing charges only
missing player resources. `NPC_WindowMoneyCheck` exempts characters below
the threshold. `NPC_WindowHealerAllHeal` restores the selected player resources and
also restores carried pets, emitting player HP/MP and per-pet status packets.
Use those authoritative packets for reconciliation rather than completion
window closure. The existing funding patch replaces the charge checks in
this NPC, but a real nurse interaction with policy installation, exact fee
verification and post-heal observation remains unverified.

The deterministic `npc.heal` skill now implements HP restoration for a
character already beside a reviewed nurse. `GameplayConfig.Healers` supplies
server-owned NPC identity, charge-threshold level and native HP rate. It is
empty by default; this does not mark either candidate nurse execution-verified
or install a live NPC contract. The action accepts only the NPC alias and its
separate maximum cost. Reviewed task files can use a `heal` step with the
`character_hp_full` completion condition.

The skill follows native menu 220 (`data="2"`) and confirmation 221 (YES 4),
checks the visible actor/window and quote before the paid write, and waits
for observed full HP. A success window without HP recovery is insufficient.
Every packet uses the existing control gate and revision fence; uncertain
writes are returned without replay. Tests cover quote truncation/free-level
rules, changed identity or level, insufficient funds, maximum cost and unknown
confirmation. This restores player HP only. Nurse selection is implemented
by the recovery skill below; wilderness evacuation, inserting resupply into
the gift plan and reconciling a live healer ledger charge remain incomplete.

The final write callback also rechecks position, level and maximum HP, even
when a changed level would produce the same truncated quote. Warp-source W
retries recheck the destination with the refreshed health sample. Parent
verification passed the repository Go suite, final service/automation race
suite and relevant vet checks. Logs: `build/ai/healer-all-tests.log`,
`build/ai/healer-race.log`, `build/ai/healer-warp-focused.log`. These are local
automated tests; no additional live nurse or gift exchange execution is claimed.

`npc.recover` now selects a configured nurse within the action's spending
limit, chooses a legal tile within the reviewed interaction range and walks there before HP
healing. A reviewed task uses kind `recover`, skill `npc.recover`, arguments
`{}`, and the `character_hp_full` success condition. The movement and healing
remain within one durable action: an unknown W or confirmation outcome does
not restart the journey. Selection ranks the routes it finds by walking steps
plus warp count, then quote, with deterministic alias/coordinate ties; it
does not claim a globally shortest route across all warp combinations.

Both selection and execution use the same navigator that blocks known
encounter tiles and nurse-occupied tiles. Warps with unsafe or non-walkable
endpoints are excluded; known sources leading to encounter tiles also cannot
be crossed on foot. Only the existing unconditional, zero-cost, time-allowed
warp rules apply. A character already in an encounter area is still refused;
portable remedies and wilderness evacuation remain separate unimplemented
work. Unexpected encounters stop recovery rather than starting another escape
loop.

The protected NPC catalog supports optional `healer` metadata with
`paid_from_level` and `hp_rate_milli` (native resolved integer rate, not a
floating-point multiplier). Existing source-fingerprint and verification
checks still apply. The runtime derives `GameplayConfig.Healers` from these
entries automatically; ordinary NPC entries do not enable healing. Rates are
validated and copied when normalizing the catalog. No production registry
entry was installed by this change.

Independent source/map verification identified a village route to
萨姆吉尔的护士: floor 1000 `(56,124)` to `(81,66)` (58 walking steps), the
unconditional warp to floor 1005 `(16,21)`, then `(17,15)` (6 steps). The nurse
stands at `(17,13)` and has range 2 (`shop_m.create:117–125`, arguments
`10|0.5|2.0|2`). Searching only one-tile neighbors would miss the accessible
side of the counter; candidate enumeration now uses the full configured
range. The two ground routes have no positive-probability encounter cells
in the loaded data. Runtime and local QA copies of NPC definitions, mapwarp,
mapset and the two floor maps match. Source-only evidence is recorded in
`build/ai/healer-route-source-evidence.json`; this is not a live treatment or
fee-confirmation result.

Repository tests, service/knowledge race tests and relevant vet passed.
Final focused race tests include the counter-range case, runtime catalog
wiring, constrained walking followed by HP restoration, unknown W refusal,
and revision refresh after read-only selection. Logs are
`build/ai/healer-recovery-all-tests.log`, `build/ai/healer-recovery-race.log`
and `build/ai/healer-recovery-final-race.log`.

Portable HP recovery source review found a meat shop at floor 1004 `(17,13)`
(`npc/genout/ss_1004_17_13`, `ItemList:2344-2347`, buy rate 1.0). The runtime
and QA item/shop definitions match; evidence is
`build/ai/portable-healing-source-evidence.json`.

| Item ID | Name | Native base HP | Price |
| --- | --- | ---: | ---: |
| 2344 | 小的肉 | 20 | 12 |
| 2345 | 乾燥肉 | 35 | 18 |
| 2346 | 大的肉 | 65 | 30 |
| 2347 | 高级肉 | 80 | 48 |

These are `ITEM_DISH`, `ITEM_FIELD_ALL`, `ITEM_TARGET_OTHER`, with
`ITEM_useRecovery` and an HP argument. The server randomizes around the base
amount and applies its recovery rate. Successful use consumes one item even
at full HP. The earlier funded item-4 purchase bought an axe and proves only
the spending path, not portable recovery.

`item.heal` now accepts a server-owned healing-item alias, obtains current
template/slot identity, uses native `ID` with target 0 (self), and requires both
one fewer matching item and increased observed HP before reporting success.
It avoids consuming at full HP and never retries an uncertain item write.
`GameplayConfig.HealingItems` supplies reviewed contracts bound to the current
knowledge fingerprint; no live item contract or automatic purchase was
installed. A reviewed task may use kind `heal_item`, skill `item.heal`, and
`{"item":"<reviewed alias>"}`. The new `character_hp_percent` success condition
can express the travel health threshold without predicting a random effect.

Native inventory updates (`I`, `S:I`, `SI`) invalidate cached AI template IDs
until another own-state response establishes them. Consumable selection uses
the new correlated `S("AI:<16 lowercase hex digits>")` query; the server
echoes `request=<id>`, and an old/unmatched response cannot authorize item use.
The original `S("AI")` remains compatible. The C module, header and modern
patch implement this extension; the observation harness and patch dry-run
pass. The isolated QA server now runs the offline GCC 13 static build at
`build/ai/inventory-query-qa-20260916/gmsv-ai-inventory-query-static-qa.exe`,
SHA-256 `51f57aa068a72148078d8cf0118fde3f9aafbfa62a0323fe936d29be9b0f2c19`.
Only `callfromcli.o` and `stoneage_ai_observation.o` differ from the reviewed
QA object set; build provenance is in that directory's
`inventory-query-provenance.json`. The running `/proc/1/exe` hash matches.
`TestLiveInventoryRefreshFreshAI` passed two consecutive correlated queries,
with distinct request IDs and AI observation revisions 29 and 30.
Inventory queries retry at most
three pre-write stale-revision failures within their shared three-second
budget; uncertain writes are not retried.

Validation passed the repository Go suite with `-p 2`, related race/vet
checks, the C observation harness and a dry-run of the updated callfromcli
patch. The initial fully parallel Go run timed out in an existing admin model
connection fixture; that fixture passed independently and the bounded-parallel
full recheck passed. Logs are `build/ai/item-healing-all-tests.log`,
`build/ai/item-healing-all-tests-recheck.log`, `build/ai/item-healing-race.log`
and `build/ai/item-healing-final-race.log`.


Real portable recovery is now verified in isolated QA by
`TestLivePortableHealingFreshAI` (opt in with
`STONEAGE_PORTABLE_HEALING_LIVE_TEST=1`). A fresh character normally bought
two item-2344 small meats at 12 stone each from floor 1004, interacting from
(17,15). The server ledger recorded one 24-stone funded charge, while visible
gold stayed at 1,000,000. This run verifies funded spending but does not claim
an insufficient-balance purchase; the earlier zero-gold run covers that case.
The character walked into a normal encounter and escaped. The test stopped
travel on observed injury, then `item.heal` consumed one meat and raised HP
from 19/35 to 35/35. After funding revocation and relogin, one meat and the
restored HP persisted. No model, HP injection, teleport, or item injection was
used. Evidence: `build/ai/portable-healing-live-evidence.json`,
`build/ai/portable-healing-live.log`, and
`build/ai/inventory-refresh-live-evidence.json`.

Admin and Web now load the reviewed healing catalog through
`STONEAGE_AI_HEALING_ITEMS` / `-ai-healing-items`; Web also accepts
`[automation].healing_items`. The control-plane Dockerfile packages
`ai/catalogs/healing-items-2.5.json`. Runtime loaders check both the knowledge
fingerprint and the effective `itemset.txt` SHA-256, since the general knowledge
fingerprint does not include that table. The builder copies contracts and
checks their knowledge binding before enabling `item.heal`.

The catalog-based live recheck passed: HP 25/35 -> 35/35, two meats -> one,
and relogin confirmed HP 35 with one meat after funding revocation. The JSON
evidence now records actual relogin values and the verified item-table hash.
Its log is `build/ai/healing-catalog-live-recheck.log`.
An earlier catalog run exposed a pre-write stale refresh during relogin and a
session event send/close panic. The opportunistic refresh now tolerates bounded
pre-write revision churn without inventing a response; event-stream shutdown
waits for active senders after waking them through `done`. Concurrent close,
blocked publication, and late publication tests cover the shutdown fix.

Task stocking was subsequently added and live-verified as described in
`ai-for-stoneage.md`; live verification of the full quest journey remains unfinished.
The full gift exchange is still unverified. Catalog configuration is optional;
no production deployment or image build was performed for this change.


Configured movement now installs `TravelItemRecovery` alongside `item.heal`.
An explicit health-check interruption can consume one reviewed backpack item,
then movement refreshes its observation and replans from the current position.
The actual encounter/warp checks decide readiness; base HP is never treated
as proof of recovery. Missing stock or any uncertain consumption result stops
the action. Recovery attempts are bounded at 15 per movement action and do not
increment or reset the separate eight-battle limit.

Fixture integration covers initial injury, injury after escape, multiple
consumptions with fresh slot identities, no consumption while already ready
or walking in a safe town, missing inventory, uncertain item use without any
subsequent W, and a non-progressing recovery bound. The existing real item-use
and catalog evidence remains valid. Automatic movement recovery subsequently
passed a separate live run described below. Gift preparation was subsequently
added and verified, while the full gift journey remains unfinished.

A separate opt-in `TestLiveMovementItemRecoveryFreshAI` now reuses the
catalog-driven purchase fixture, seeks naturally sub-half HP, and then asks
`MovementSkill` to advance one real route tile using `TravelItemRecovery`.
It requires confirmed arrival, item consumption, HP gain, and persisted
inventory/HP after relogin. The first run failed before recovery could be
exercised: the fresh character died during normal encounter/escape travel
(`ErrTravelDead`, 101 seconds). See
`build/ai/movement-item-recovery-live.log`. No passing evidence JSON was
written. This is a survival/routing gap, not proof of automatic recovery;
stock availability alone does not establish that a level-one character can
survive the gift route. Do not raise encounter limits or mark the full quest
verified on the basis of the portable-item test.



`StockSkill` now implements the native item-shop workflow as one bounded
`item.stock` action: arguments are a reviewed item alias and `target_count`
(1–15). It refreshes inventory, no-ops if already stocked, calculates only the
shortfall, checks capacity/cost/funds, reaches a reviewed interaction tile,
then recalculates the shortfall before buying. The purchase window is sent
once. A fresh correlated inventory response and the expected finite/funded
gold result are required for confirmation; ambiguous outcomes are returned
without replaying the purchase. A task step can use kind `stock`, with the
existing `item_count` condition for its target inventory.

The isolated `TestLivePortableHealingFreshAI` loads all three packaged
catalogs (NPCs, healing items, and stock offers), then uses `StockSkill`.
It autonomously reached the floor-1004 meat shop, bought two meats for one
ledger charge of 24, then naturally received damage and used one meat:
HP 28/35 -> 35/35. Relogin preserved HP 35 and one meat after funding
revocation. Evidence is `build/ai/portable-healing-live-evidence.json`
(`stock_catalog_verified: true`, `stock_skill_verified: true`) and
`build/ai/stock-catalog-live.log`. This deterministic test did not invoke a model.

`GameplayConfig.StockItems` wires reviewed offers into the shared executor.
Admin and Web now load stock offers through `STONEAGE_AI_STOCK_ITEMS`, after
loading their NPC and healing dependencies. The versioned catalog binds to
the knowledge fingerprint and resolves only verified item/NPC aliases;
the healing loader separately checks the actual itemset hash. Invalid
catalogs fail runtime initialization before automation stores are opened.
The packaged files are `ai/catalogs/shops-2.5.json`,
`ai/catalogs/healing-items-2.5.json`, and `ai/catalogs/stock-items-2.5.json`.
Automatic insertion of stocking into quest plans remains unfinished. This
run does not yet verify automatic recovery inside a complete quest journey.

The stock skill itself does not own a durable idempotency store: production
calls must remain inside the automation engine's prepared/submitted lifecycle.
A new call after an uncertain purchase is not a substitute for reconciling that
operation. Native snapshots do not expose the funding ledger's expenditure
counter; the live test independently checks its single charge, while the skill
checks authoritative inventory and finite/funded balance semantics.


### Next gate: task-specific supplies and survival

Use explicit reviewed `stock` steps in an individual task definition before
its long-distance travel, rather than appending purchases to every request
in `QuestPlans.Task`. The existing planner includes declared step costs and
timeouts and requires verified task evidence. Keep request parameters closed:
the model must not supply item templates, shop prices, or verification flags.
Any reviewed supply quantity must reserve backpack space for quest items;
the skill limit of 15 is a protocol bound, not a recommended purchase count.
Purchasing changes the starting position to the shop, so route verification
must cover that position. Task evidence and dependent catalog fingerprints
must be regenerated together when a task definition changes.

Do not enable the gift task merely by adding food. First establish a route
and character preparation that survive the native encounters, then verify
pickup, automatic recovery, delivery, and saved completion together. The
first automatic-recovery live failure above remains a counterexample to
treating food purchase as sufficient preparation, despite the later successful
single-recovery run.


### Automatic movement recovery: separate live proof

`TestLiveMovementItemRecoveryFreshAI` passed after its route preparation was
corrected to obtain the expected revision after computing the next tile.
The prior diagnostic run reached HP 8/35 after escaping an eight-enemy,
level-4–6 encounter at floor 100 `(422,479)`, then stopped on a stale revision.
The test now samples again after route computation and confirms that the
position has not changed. It does not replay movement or item use. Movement
errors now retain their underlying error identity while identifying initial
observation versus item-recovery failures.

The passing run naturally escaped five encounters, reaching HP 10/35. A
single `MovementSkill.Execute` request caused production `TravelItemRecovery`
to consume one catalog-verified meat and resume to floor 100 `(310,515)`.
Authoritative arrival was checked; HP became 28/35 and the backpack retained
one meat. Funding was revoked and a new login confirmed HP 28 and one meat.
Evidence: `build/ai/movement-item-recovery-live-evidence.json`,
`build/ai/movement-item-recovery-fresh-route.log`, and
`build/ai/travel-diagnostic-2436902922.json`. No model was invoked and no HP,
position, or items were injected.

The opt-in diagnostic wrapper records only observations already read by the
gameplay flow, with a 512-record bound and without names, credentials or raw
packets. It forwards native map-event acknowledgements; the initial diagnostic
wrapper omission was fixed before the passing run. Failed runs remain in
`build/ai/movement-item-recovery-diagnostic-live.log` and
`build/ai/movement-item-recovery-diagnostic-recheck.log`.

This proves one autonomous field recovery with resumed walking and saved
state, not survival of arbitrary encounters or completion of the gift quest.
The route/preparation gap remains: ordinary hometown-0 exits enter encounter
area 21, whose data references missing group 1230. The QA native bounds patch
returns no encounter when that missing group is selected, but the portable
knowledge model does not certify this build-specific behavior as a verified
leveling area. Existing routes toward area 28 also cross stronger groups.
Resolving that source/runtime discrepancy and verifying reachable beginner
training remain necessary before advertising an autonomous full gift journey.


### Missing group 1230 provenance recheck

The original `vendor/archives/stoneage2.5.tar.gz`, checked-in 2.5 source data,
and current runtime data agree: `group.txt` contains 724 group IDs and
`group1.txt` contains 723; neither defines group ID 1230. The current and
source `group.txt` SHA-256 is
`b1947ee3cad62d9f67d7aa0a4af0258c6d7fd35365883fc0fca0b9ec7ffee926`.
Group 671 references **enemy** ID 1230; that enemy exists, but it cannot
substitute for a missing **group** definition. Area 21 references groups
88/91/89/92/1230 with weights 50/50/10/10/100. The designated 8.5 reference
has no corresponding data table; its server archive contains source only.

The native bounds patch returns NULL for the missing selected group, and
battle creation maps that to `BATTLE_ERR_NOENEMY`. It prevents invalid indexing
without inventing a group or changing the configured encounter rates. A data
repair requires an authoritative same-version group definition and verified
enemy/enemybase references; alternatively, any future use of the patched
no-encounter semantics must explicitly bind evidence to that server behavior.
No guessed replacement or data change was made during this recheck.

### Riding dialog contract draft

Four source-derived trainer contracts are recorded in
`ai/catalogs/drafts/riding-2.5.json`, with immutable source hashes and the
menu mapping in its README. All entries remain `verified=false`; the runtime
loader rejects the draft. Offline tests resolve the existing task choices
against local copies through the production parser and check that all three
packaged gameplay catalogs match the effective knowledge snapshot.

The embedded task now distinguishes buying the course from mounting a pet,
corrects menu source ranges, and explicitly retains incomplete preparation.
Its byte change required catalog rebinding; comparison of complete knowledge
file manifests found only `tasks/riding-basic.json` changed. No shop/item data,
runtime verification flag or live acceptance result changed.

### Adult ceremony task declaration and consumption checks

`internal/aiknowledge/tasks/adult-ceremony.json` now declares the 15-step
entrance, request, ritual-item grant and reward exchange flow. The automatic
departure requirement is level 35, chosen from the guide recommendation
(`https://news.17173.com/z/stoneage/renwu/n10.htm`); the source minimum remains
level 30 and is rechecked at the entrance action. This is a policy distinction,
not a claim that `FREE:LV>29` means 35. No pet minimum was invented.

Departure requires an authoritative solo state, fifteen empty backpack slots,
and known-clear current/end event 4 flags. Every action retains the solo
check; the messenger's grant rechecks event 4, fifteen free slots and absence
of existing ritual items immediately before confirmation. It never auto-sells,
drops or stores the player's items. Generic meat stocking is excluded because
it conflicts with receiving fifteen separate items. Existing checkpoint
recovery is the path for an interrupted task; a fresh start does not adopt an
already-running event.

Completion requires all three: end event 4, reward 2418, and no ritual items
2417. The new shared `item_absent` condition accepts missing keys or explicit
zero only in a connected/ready observation with `inventory:known` and a
non-nil authoritative inventory map. Unknown inventory, disconnected state,
positive and invalid negative counts cannot prove consumption. Its Web text
is human-readable. This closes the gap where a dialog/reward alone could
mask failure to consume the ritual items.

`ai/catalogs/drafts/adult-ceremony-2.5.json` records the entrance (271/YES),
judge request (234/YES then 231/OK), and both acceptance flows
(235/YES then 430/OK). These are source-derived contracts for the preserved
`_NEWEVENT` build, not observed live dialogs; all `verified` flags remain false
and the runtime loader refuses the draft. Source lines and limitations are
listed in `ai/catalogs/drafts/README.md`.

The planner tests compile a local test copy, exercising actual preflight for
level, unlimited funds, solo state and inventory constraints, as well as the
three-part completion check. They do not promote the installed task. Missing
route/preparation approval and full execution acceptance continue to block it.
In particular, the guide's south-island travel branch includes a 200-stone
pass; the current zero NPC-interaction budget is not a reviewed arbitrary-start
travel budget. A route and its costs must be chosen before promotion.
