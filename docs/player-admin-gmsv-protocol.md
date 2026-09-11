# GMSV player-admin file bridge

The administrator talks to an online GMSV through a private fixed-path file
queue. The bridge runs in the existing GMSV main loop; it does not open a
network listener and never executes request content as a command.

The default queue is `/run/stoneage/player-admin/gmsv`. A deployment may set
`STONEAGE_PLAYER_ADMIN_DIR` in the GMSV environment to an absolute directory.
The bridge creates `requests/` and `responses/` below that directory. The
administrator writes a temporary request and atomically renames it to
`requests/<id>.req`; GMSV atomically renames its temporary response to
`responses/<id>.resp` and then removes the request.

Request IDs are one to 64 ASCII letters, digits, `.`, `_` or `-`. They are
used as filenames and are never accepted from a path component. At most eight
requests are consumed per GMSV main-loop turn.

## Encoding and common fields

Each request and response is a sequence of `key=value` lines. Values are
percent encoded over the original bytes. The bridge therefore preserves
CP936/GBK names without allowing a newline or `=` to change the record. The
unreserved bytes `A-Z`, `a-z`, `0-9`, `-`, `_`, `.`, and `~` may remain
unescaped; every other byte is `%HH` with uppercase hexadecimal digits.

Every request includes:

```text
protocol=1
id=<request id>
action=<action>
```

Every online-character request also includes `account`, `character`, and
`character_slot`. `character_slot` is the SAAC save slot (`0` or `1`) used to
identify the character. It is separate from the asset `slot` field.

`slot` means an inventory/warehouse item or pet slot. `snapshot`,
`set_character`, `grant_item`, and `grant_pet` may omit it; grant actions then
choose the first available slot. `set_item`, `delete_item`, `set_pet`,
`delete_pet`, `set_pet_skill`, and `delete_pet_skill` require it. `location` is
`inventory` (the default) or `warehouse`.

Mutations must include `expected_revision` from the latest snapshot. The
optional `expected_sequence` is checked as an additional compatibility guard.
Item and pet mutations may also include `expected_item_id` or
`expected_pet_id`. A mismatch returns `stale_target`, `stale_item`, or
`stale_pet` without changing the character.

## Actions

`snapshot` returns the online character's attributes and all occupied
inventory/warehouse item and pet slots. It includes `attribute.<name>`,
`item.<location>.<slot>.*`, `pet.<location>.<slot>.*`, `character_slot`,
`sequence`, and a 16-character FNV-1a `revision` derived from the complete
legacy character serialization.

`set_character` changes one supported character attribute with `field` and
integer `value`. Supported names include gold (`gld`, `bankgld`,
`personaglod`), level/experience/HP/MP, base attributes, points, elemental
attributes, transmigration, duel points, fame, and member points when the
corresponding legacy feature is enabled.

`grant_item` creates `quantity` independent native item objects using the
native item template initializer and puts one object in each reserved slot.
It never writes a stack count into an equipment item. `set_item` changes a
supported native item integer field; `field=name` with `name` renames an item
through the native `ITEM_NAME` field. `upin`/`quantity` can be changed only
when the item is marked stackable. `delete_item` removes the item and first
moves equipped items to an empty inventory slot.

`grant_pet` creates `quantity` independent native pets and reserves every
destination slot before creating any of them. `template_id` is the
`enemybase.txt` `TempNo`. `set_pet` changes a supported pet attribute or uses
`field=name` with a percent-encoded `name`. Pet fields use their native
meanings: `chr` is base loyalty, `luc` is the signed loyalty variation, `slt`
is the skill-slot count (1–7), `llt` is the growth rank (0–5), and `lvup` is
the packed native allocation data. Player money/point fields are not accepted
for pet edits.
`delete_pet` removes the pet and clears default/follow/riding references.
`set_pet_skill` and `delete_pet_skill` set or clear a validated native pet
skill at `skill_slot`.

`export_item` and `export_pet` are template-only requests used by an offline
manager. They require only `template_id`, create a temporary native object,
and return its native serialized `payload`; they do not target a character or
persist the temporary object.

`inspect_item` accepts a percent-encoded native item `payload`, hydrates it
with `ITEM_makeExistItemsFromStringToArg` (which applies template defaults
before overlaying simplified save fields), and returns complete
`item.inspected.*` fields including `canpile` and the effective stack count.
The temporary object is discarded.

## Responses

Responses begin with `protocol=1`. Success responses contain `ok=1`,
`code=ok`, and `id`. Online-character success responses also contain the
`character_slot`, current `sequence`, and current `revision`. Mutation
responses contain `save_status=queued`: the existing GMSV-to-SAAC save is
asynchronous, so this means the save request was accepted by GMSV, not that
SAAC has already finished writing the archive. A snapshot contains
`save_status=not_requested`.

Grant responses additionally return the selected asset `slot` and its native
`item_id` or `pet_id`. Export responses return `kind`, `template_id`, and the
percent-encoded native `payload`.

Errors contain `ok=0`, a machine-readable `code`, and an encoded `message`.
The bridge uses these codes for malformed input or unsupported actions,
`target_not_online`, `ambiguous_target`, `expected_revision_required`,
`stale_target`, `busy_battle`, `busy_trade`, `not_connected`, inventory or
warehouse capacity, invalid templates/fields/skills, stale item/pet IDs, and
save failure. A mutation is refused while the character is fighting or
trading, or when it has no client connection.

The response is published only after the complete file is written and closed;
partial files are never visible under the final `.resp` name.
