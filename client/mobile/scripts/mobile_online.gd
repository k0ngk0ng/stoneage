extends Node2D
## Portrait client for the local StoneAge gateway.
##
## This scene deliberately keeps the phone UI small, but the game state is not
## local simulation: login, character entry, movement, talk and encounters all
## go through the function-name protocol used by the preserved PC client.

const Protocol = preload("res://scripts/stoneage_named_protocol.gd")
# The PNGs remain in the bundle as an extraction/debug fallback, but an online
# world is rendered from the server's M rectangle below.  The server sends
# bitmap numbers, while this manifest supplies the extracted bitmap offsets
# needed to place those numbers exactly like the PC client.
const ASSET_MANIFEST_PATH := "res://assets/generated/manifest.json"
const MAP_200_TEXTURE: Texture2D = preload("res://assets/generated/maps/map_200.png")
const MAP_1006_TEXTURE: Texture2D = preload("res://assets/generated/maps/map_1006.png")
const BATTLE_TEXTURE: Texture2D = preload("res://assets/generated/battle/battle_00.png")
# Player graphics live in the extracted PC sprite sheets; 8 directions × 1 idle
# frame per direction and 8 directions × 6 walk frames per direction. Each
# direction's walk cycle is a contiguous run of bmp numbers starting at the
# base id listed below. Loaded at runtime so a new player graphic only needs
# a base pair added to PLAYER_FRAME_BASES.
const PLAYER_FRAME_BASES := {
	100000: {"idle": [10177, 10220, 10263, 10306, 10349, 10392, 10435, 10478],
		"walk": [10212, 10255, 10298, 10341, 10384, 10427, 10470, 10513]},
	100025: {"idle": [17818, 17861, 17904, 17947, 17990, 18033, 18076, 18119],
		"walk": [17852, 17895, 17938, 17981, 18024, 18067, 18110, 18153]},
}
const PLAYER_WALK_FRAMES_PER_DIR := 6
const WALK_FRAME_INTERVAL := 0.09
const DEFAULT_PLAYER_GRAPHIC := 100025
const NPC_TEXTURE: Texture2D = preload("res://assets/generated/bitmaps/bitmap_5911.png")
const MONSTER_TEXTURE: Texture2D = preload("res://assets/generated/bitmaps/bitmap_95999.png")
const UI_FONT = preload("res://assets/fonts/NotoSansCJKsc-Regular.otf")
const CP936_TABLE_PATH := "res://assets/generated/cp936_table.bin"

const BATTLE_RENDER_ORIGIN := Vector2(896.0, 256.0)
const TILE_STEP := Vector2(32.0, 24.0)
const CG_INVISIBLE := 99
const VIEWPORT_SIZE := Vector2(390.0, 844.0)

# LOGIN.H's WN window types.  Only these four are player-facing message
# windows; SELECT/shop/pet windows carry menu serialization that must not be
# dumped into the compact phone chat transcript.
const WN_MESSAGE_TYPES := [0, 1, 10, 11]

# These are the active values in the preserved PC MAP.H/MAP.CPP build.  The
# server's MC checksum rectangle is intentionally smaller (27x27); it is a
# cache/visibility handshake, not the full rectangle the PC renderer paints.
# Requesting this rectangle through M keeps the phone's visible world aligned
# with the same coordinate window while still making the server authoritative.
const PC_MAP_TILE_GRID_X1 := -20
const PC_MAP_TILE_GRID_X2 := 17
const PC_MAP_TILE_GRID_Y1 := -16
const PC_MAP_TILE_GRID_Y2 := 21

const BG_WORLD := Color("#182b2b")
const BG_BATTLE := Color("#141b24")
const PANEL := Color("#112022e6")
const PANEL_LIGHT := Color("#254444f2")
const ACCENT := Color("#f1bd64")
const GOOD := Color("#77d49b")
const DANGER := Color("#f18470")
const TEXT := Color("#f6f1df")
const MUTED := Color("#a8c2bb")

var map_sprite: Sprite2D
var map_layers_root: Node2D
var map_tile_layer: Node2D
var map_object_layer: Node2D
var battle_sprite: Sprite2D
var player_sprite: Sprite2D
var npc_sprite: Sprite2D
var battle_player_sprite: Sprite2D
var battle_enemy_sprite: Sprite2D
var camera: Camera2D

var ui: Control
var top_panel: Panel
var location_label: Label
var network_label: Label
var actor_label: Label
var event_label: Label
var world_controls: Control
var battle_controls: Control
var login_controls: Control
var character_controls: Control
var dialogue_panel: Panel
var dialogue_label: Label
var dialogue_title_label: Label
var chat_toggle_button: Button
var chat_input: LineEdit
var chat_send_button: Button
var battle_panel: Panel
var battle_log_label: Label
var battle_status_label: Label
var account_edit: LineEdit
var password_edit: LineEdit
var login_status_label: Label
var character_status_label: Label
var character_option: OptionButton
var attack_button: Button

var tcp: StreamPeerTCP
var receive_buffer := PackedByteArray()
var cp936_table := PackedByteArray()
var network_phase := "offline"
var account := ""
var password := ""
var character := ""
var character_names: Array[String] = []
var next_message_id := 1
var server_floor := -1
var server_position := Vector2i.ZERO
var server_direction := 0
var player_cell := Vector2i.ZERO
var pending_move := false
var pending_move_started_ms := 0
var pending_move_origin := Vector2i.ZERO
var pending_move_target := Vector2i.ZERO
var pending_move_direction := ""
var pending_move_snapshot_retries := 0
var pending_move_last_snapshot_ms := 0
var player_graphic_id := DEFAULT_PLAYER_GRAPHIC
var player_walking := false
var walk_frame_index := 0
var walk_frame_time := 0.0
var walk_animation_direction := -1
var walk_target := Vector2i(-1, -1)
var player_idle_frames: Dictionary = {}
var player_walk_frames: Dictionary = {}
var cursor_sprite: Node2D
var cursor_target_cell := Vector2i(-1, -1)
var pointer_held := false
var pointer_last_screen := Vector2.ZERO
var map_crop_origin := Vector2i.ZERO
var map_crop_size := Vector2i.ZERO
var map_render_origin := Vector2.ZERO
var map_loaded := false
var map_window_floor := -1
var map_window_origin := Vector2i.ZERO
var map_window_size := Vector2i.ZERO
var map_window_name := ""
var map_window_tile_ids: Array = []
var map_window_object_ids: Array = []
var map_window_event_ids: Array = []
var map_window_key := ""
var map_checksum_key := ""
var map_window_missing_bitmaps := 0
var map_checksum_rect := Rect2i(-1, -1, 0, 0)
var map_checksum_tiles := 0
var map_checksum_objects := 0
var map_checksum_events := 0
var map_pending_request_key := ""
var bitmap_manifest: Dictionary = {}
var bitmap_aliases: Dictionary = {}
var map_manifest: Dictionary = {}
var resource_map_manifest: Dictionary = {}
var bitmap_texture_cache: Dictionary = {}
var missing_bitmap_ids: Dictionary = {}
var server_position_received := false
var npc_cell := Vector2i(-1, -1)
var npc_global := Vector2i(-1, -1)
var npc_name := ""
var player_object_index := -1
var actor_count := 0
var actor_sprites: Dictionary = {}
var actor_cells: Dictionary = {}
var mode := "login"
var battle_target_bid := 10
var battle_my_bid := 0
var battle_enemy_hp := -1
var battle_enemy_max_hp := -1
var battle_log: Array[String] = []
var chat_lines: Array[String] = []
var pending_chat_messages: Array[String] = []
var chat_expanded := false
var chat_unread_count := 0


func _ready() -> void:
	RenderingServer.set_default_clear_color(BG_WORLD)
	_load_cp936_table()
	_load_asset_manifest()
	_load_player_frames()
	_build_world()
	_build_battle()
	_build_ui()
	_show_login()


func _load_player_frames() -> void:
	for graphic_id in PLAYER_FRAME_BASES.keys():
		var bases: Dictionary = PLAYER_FRAME_BASES[graphic_id]
		player_idle_frames[graphic_id] = _build_frame_row(bases["idle"], 1)
		player_walk_frames[graphic_id] = _build_frame_row(bases["walk"], PLAYER_WALK_FRAMES_PER_DIR)
	_load_manifest_sprites()


const SPRITE_ACTION_STAND := 3
const SPRITE_ACTION_WALK := 4


func _load_manifest_sprites() -> void:
	# asset_cooker emits per-graphic action tables keyed by ANIM_LIST from the
	# PC client's loadsprbin.h: 0=ATTACK, 3=STAND (idle), 4=WALK.  pattern.cpp
	# falls back to STAND when the requested action is missing, so treat it as
	# the canonical idle set here rather than action 0 (attack).
	var file := FileAccess.open(ASSET_MANIFEST_PATH, FileAccess.READ)
	if file == null:
		return
	var parsed = JSON.parse_string(file.get_as_text())
	if not parsed is Dictionary:
		return
	var sprites = parsed.get("sprites", {})
	if not sprites is Dictionary:
		return
	for key in sprites.keys():
		var graphic_id := int(str(key))
		var entry: Dictionary = sprites[key]
		var actions = entry.get("actions", [])
		if not actions is Array:
			continue
		var idle_rows: Array = []
		var walk_rows: Array = []
		idle_rows.resize(8)
		walk_rows.resize(8)
		for i in range(8):
			idle_rows[i] = []
			walk_rows[i] = []
		for action_entry in actions:
			if not action_entry is Dictionary:
				continue
			var action_id := int(action_entry.get("action", -1))
			var direction := clampi(int(action_entry.get("direction", 0)), 0, 7)
			var frames = action_entry.get("frames", [])
			if not frames is Array:
				continue
			var textures: Array = []
			for frame in frames:
				if not frame is Dictionary:
					continue
				var path := "res://assets/generated/" + str(frame.get("file", ""))
				var tex = load(path)
				if tex is Texture2D:
					textures.append(tex)
			if action_id == SPRITE_ACTION_STAND and not textures.is_empty():
				idle_rows[direction] = textures
			elif action_id == SPRITE_ACTION_WALK and not textures.is_empty():
				walk_rows[direction] = textures
		if not player_idle_frames.has(graphic_id) and _rows_have_any(idle_rows):
			player_idle_frames[graphic_id] = idle_rows
		if not player_walk_frames.has(graphic_id) and _rows_have_any(walk_rows):
			player_walk_frames[graphic_id] = walk_rows


func _rows_have_any(rows: Array) -> bool:
	for row in rows:
		if row is Array and not row.is_empty():
			return true
	return false


func _build_frame_row(bases: Array, frames_per_dir: int) -> Array:
	var directions: Array = []
	for base in bases:
		var dir_frames: Array = []
		for offset in range(frames_per_dir):
			var path := "res://assets/generated/bitmaps/bitmap_%d.png" % (int(base) + offset)
			var texture := load(path)
			if texture is Texture2D:
				dir_frames.append(texture)
		directions.append(dir_frames)
	return directions


# Server/wire direction is 0..7 with 0=N (matches the a..h letter table).
# The sprite binary indexes frames by the PC client's internal dir 0..7
# where 0=SW.  netproc.cpp:2262/2675 does `dir = (wire + 3) % 8` before
# handing the value to the renderer; do the same here so wire dir 0 (N)
# looks up the sprite row cooked as PC dir 3 (N).
func _sprite_dir(wire_direction: int) -> int:
	return posmod(wire_direction + 3, 8)


func _player_idle_texture(graphic_id: int, direction: int) -> Texture2D:
	var frames: Array = player_idle_frames.get(graphic_id, [])
	if frames.is_empty():
		frames = player_idle_frames.get(DEFAULT_PLAYER_GRAPHIC, [])
	if frames.is_empty():
		return null
	var dir_frames: Array = frames[clampi(_sprite_dir(direction), 0, frames.size() - 1)]
	return dir_frames[0] if not dir_frames.is_empty() else null


func _player_walk_texture(graphic_id: int, direction: int, frame: int) -> Texture2D:
	var frames: Array = player_walk_frames.get(graphic_id, [])
	if frames.is_empty():
		frames = player_walk_frames.get(DEFAULT_PLAYER_GRAPHIC, [])
	if frames.is_empty():
		return _player_idle_texture(graphic_id, direction)
	var dir_frames: Array = frames[clampi(_sprite_dir(direction), 0, frames.size() - 1)]
	if dir_frames.is_empty():
		return _player_idle_texture(graphic_id, direction)
	return dir_frames[frame % dir_frames.size()]


func _actor_idle_texture(graphic_id: int, direction: int) -> Texture2D:
	# Same as _player_idle_texture but without falling back to the player's
	# default graphic — an unknown actor should render as the neutral NPC
	# placeholder rather than a player_demo clone.
	var frames: Array = player_idle_frames.get(graphic_id, [])
	if frames.is_empty():
		return null
	var dir_frames: Array = frames[clampi(_sprite_dir(direction), 0, frames.size() - 1)]
	return dir_frames[0] if not dir_frames.is_empty() else null


func _clear_pending_move() -> void:
	pending_move = false
	pending_move_started_ms = 0
	pending_move_origin = Vector2i.ZERO
	pending_move_target = Vector2i.ZERO
	pending_move_direction = ""
	pending_move_snapshot_retries = 0
	pending_move_last_snapshot_ms = 0


func _process(delta: float) -> void:
	_poll_network()
	if mode == "world" and camera != null and map_loaded and server_position_received:
		camera.position = _cell_to_world(player_cell, map_render_origin)
	if player_walking:
		walk_frame_time += delta
		while walk_frame_time >= WALK_FRAME_INTERVAL:
			walk_frame_time -= WALK_FRAME_INTERVAL
			walk_frame_index = (walk_frame_index + 1) % PLAYER_WALK_FRAMES_PER_DIR
			var direction := clampi(server_direction, 0, 7)
			var texture := _player_walk_texture(player_graphic_id, direction, walk_frame_index)
			if texture != null:
				player_sprite.texture = texture
	if pending_move and pending_move_started_ms > 0:
		var now_ms := Time.get_ticks_msec()
		if pending_move_snapshot_retries < 1 and now_ms - pending_move_last_snapshot_ms > 700:
			# A C request is cheap and ordered after W on the same TCP stream. A
			# retry covers a delayed legacy tick without turning a dropped snapshot
			# into a permanently disabled input path.
			pending_move_snapshot_retries += 1
			pending_move_last_snapshot_ms = now_ms
			_request_player_snapshot()
		if now_ms - pending_move_started_ms > 2500:
			_clear_pending_move()
			player_walking = false
			_clear_walk_target()
			_set_player_texture(false)
			event_label.text = "服务器没有确认这次移动，请稍后重试。"


func _build_world() -> void:
	map_layers_root = Node2D.new()
	map_layers_root.name = "ServerMapLayers"
	map_layers_root.visible = false
	add_child(map_layers_root)
	map_tile_layer = Node2D.new()
	map_tile_layer.name = "ServerTiles"
	map_layers_root.add_child(map_tile_layer)
	map_object_layer = Node2D.new()
	map_object_layer.name = "ServerMapObjects"
	map_layers_root.add_child(map_object_layer)

	map_sprite = Sprite2D.new()
	map_sprite.name = "ServerMap"
	map_sprite.centered = false
	map_sprite.position = Vector2.ZERO
	map_sprite.visible = false
	add_child(map_sprite)

	player_sprite = Sprite2D.new()
	player_sprite.name = "player_demo"
	player_sprite.texture = _player_idle_texture(DEFAULT_PLAYER_GRAPHIC, 4)
	player_sprite.z_index = 20
	player_sprite.visible = false
	add_child(player_sprite)

	npc_sprite = Sprite2D.new()
	npc_sprite.name = "ServerNPC"
	npc_sprite.texture = NPC_TEXTURE
	npc_sprite.z_index = 18
	npc_sprite.visible = false
	add_child(npc_sprite)

	cursor_sprite = _make_cursor_sprite()
	add_child(cursor_sprite)

	camera = Camera2D.new()
	camera.name = "PortraitCamera"
	camera.position_smoothing_enabled = true
	camera.position_smoothing_speed = 8.0
	camera.zoom = Vector2(1.25, 1.25)
	add_child(camera)
	camera.make_current()


func _build_battle() -> void:
	battle_sprite = Sprite2D.new()
	battle_sprite.name = "ServerBattleMap00"
	battle_sprite.texture = BATTLE_TEXTURE
	battle_sprite.centered = false
	battle_sprite.position = Vector2.ZERO
	battle_sprite.visible = false
	add_child(battle_sprite)

	battle_player_sprite = Sprite2D.new()
	battle_player_sprite.name = "BattlePlayer"
	battle_player_sprite.texture = _player_idle_texture(DEFAULT_PLAYER_GRAPHIC, 4)
	battle_player_sprite.z_index = 20
	battle_player_sprite.visible = false
	add_child(battle_player_sprite)

	battle_enemy_sprite = Sprite2D.new()
	battle_enemy_sprite.name = "BattleMonster"
	battle_enemy_sprite.texture = MONSTER_TEXTURE
	battle_enemy_sprite.z_index = 20
	battle_enemy_sprite.visible = false
	add_child(battle_enemy_sprite)


func _build_ui() -> void:
	var layer := CanvasLayer.new()
	layer.name = "PortraitUI"
	add_child(layer)
	ui = Control.new()
	ui.name = "Root"
	ui.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	# Root must not swallow taps meant for tap-to-walk; child panels/buttons still
	# consume their own hits because they default to MOUSE_FILTER_STOP.
	ui.mouse_filter = Control.MOUSE_FILTER_IGNORE
	layer.add_child(ui)

	var shade := ColorRect.new()
	shade.color = Color(0, 0, 0, 0.12)
	shade.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	shade.mouse_filter = Control.MOUSE_FILTER_IGNORE
	ui.add_child(shade)

	top_panel = _panel(ui, Rect2(12, 12, 366, 86), PANEL, 16)
	_center_top(top_panel, 366, 12, 86)
	_add_label(top_panel, "STONEAGE  ·  ONLINE", Rect2(16, 8, 330, 24), 18, ACCENT)
	location_label = _add_label(top_panel, "等待服务器地图…", Rect2(16, 38, 330, 22), 13, TEXT)
	network_label = _add_label(top_panel, "未连接", Rect2(16, 61, 330, 19), 12, MUTED)

	var actor_panel := _panel(ui, Rect2(12, 108, 366, 52), PANEL, 14)
	actor_panel.name = "ActorPanel"
	_center_top(actor_panel, 366, 108, 52)
	actor_label = _add_label(actor_panel, "正在等待服务器角色/NPC数据…", Rect2(14, 13, 338, 26), 13, MUTED)

	event_label = _add_label(ui, "", Rect2(18, 172, 354, 46), 13, TEXT)
	event_label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
	event_label.mouse_filter = Control.MOUSE_FILTER_IGNORE

	world_controls = Control.new()
	world_controls.name = "WorldControls"
	world_controls.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	# The world layer must not eat taps in the empty play field; tap-to-walk is
	# routed through _unhandled_input on this Node2D. Individual UI panels still
	# consume their own hits.
	world_controls.mouse_filter = Control.MOUSE_FILTER_IGNORE
	ui.add_child(world_controls)

	dialogue_panel = _panel(ui, Rect2(12, 634, 366, 210), Color("#102022f5"), 18)
	_center_bottom(dialogue_panel, 366, 210, 0)
	dialogue_panel.visible = false
	dialogue_title_label = _add_label(dialogue_panel, "聊天", Rect2(16, 12, 180, 24), 16, ACCENT)
	chat_toggle_button = _button(dialogue_panel, "展开", Rect2(266, 8, 84, 34), Callable(self, "_toggle_chat"), PANEL_LIGHT, 13)
	chat_toggle_button.add_theme_color_override("font_color", TEXT)
	dialogue_label = _add_label(dialogue_panel, "", Rect2(16, 46, 334, 88), 14, TEXT)
	chat_input = _line_edit(dialogue_panel, "输入聊天内容", Rect2(16, 148, 238, 42), false)
	chat_input.add_theme_font_size_override("font_size", 14)
	chat_input.text_submitted.connect(_on_chat_submitted)
	chat_send_button = _button(dialogue_panel, "发送", Rect2(264, 148, 86, 42), Callable(self, "_on_chat_send"), PANEL_LIGHT, 14)
	chat_send_button.add_theme_color_override("font_color", TEXT)
	_set_chat_expanded(false)

	battle_controls = Control.new()
	battle_controls.name = "BattleControls"
	battle_controls.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	battle_controls.visible = false
	ui.add_child(battle_controls)
	battle_panel = _panel(battle_controls, Rect2(12, 520, 366, 292), Color("#111921f5"), 18)
	_center_bottom(battle_panel, 366, 292, 32)
	_add_label(battle_panel, "服务器遭遇战", Rect2(16, 12, 260, 24), 18, ACCENT)
	battle_status_label = _add_label(battle_panel, "等待战斗状态…", Rect2(16, 45, 334, 24), 14, TEXT)
	battle_log_label = _add_label(battle_panel, "服务器正在准备战斗。", Rect2(16, 76, 334, 62), 14, MUTED)
	attack_button = _button(battle_panel, "攻击", Rect2(16, 150, 106, 50), Callable(self, "_on_attack"), Color("#713d3c"), 16)
	_button(battle_panel, "防御", Rect2(130, 150, 106, 50), Callable(self, "_on_guard"), Color("#36546a"), 16)
	_button(battle_panel, "逃跑", Rect2(244, 150, 106, 50), Callable(self, "_on_escape"), Color("#394047"), 16)
	var battle_note := _add_label(battle_panel, "命令通过 B 发送；服务器决定伤害和胜负", Rect2(16, 224, 334, 30), 12, MUTED)
	battle_note.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER

	login_controls = Control.new()
	login_controls.name = "LoginControls"
	login_controls.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	login_controls.visible = false
	ui.add_child(login_controls)
	var login_panel := _panel(login_controls, Rect2(18, 104, 354, 500), Color("#102022f8"), 24)
	_center_top(login_panel, 354, 104, 500)
	_add_label(login_panel, "STONEAGE", Rect2(28, 28, 298, 42), 30, ACCENT)
	_add_label(login_panel, "真实本地网关 · 竖屏客户端", Rect2(30, 74, 298, 28), 15, MUTED)
	_add_label(login_panel, "账号", Rect2(30, 126, 120, 24), 13, MUTED)
	account_edit = _line_edit(login_panel, "例如 player_demo", Rect2(30, 151, 294, 50), false)
	_add_label(login_panel, "密码", Rect2(30, 216, 120, 24), 13, MUTED)
	password_edit = _line_edit(login_panel, "密码", Rect2(30, 241, 294, 50), true)
	var login_button := _button(login_panel, "连接并登录", Rect2(30, 326, 294, 56), Callable(self, "_on_login_pressed"), PANEL_LIGHT, 17)
	login_button.add_theme_color_override("font_color", TEXT)
	login_status_label = _add_label(login_panel, "请输入账号和密码", Rect2(30, 398, 294, 28), 13, MUTED)
	login_status_label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
	# Keep the idle login page clean.  Validation/transport errors make this
	# label visible temporarily; the normal page should end at the button.
	login_status_label.visible = false

	character_controls = Control.new()
	character_controls.name = "CharacterControls"
	character_controls.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	character_controls.visible = false
	ui.add_child(character_controls)
	var character_panel := _panel(character_controls, Rect2(18, 190, 354, 360), Color("#102022f8"), 24)
	_center_top(character_panel, 354, 190, 360)
	_add_label(character_panel, "选择人物", Rect2(28, 28, 298, 42), 27, ACCENT)
	_add_label(character_panel, "账号验证成功，请选择要登入的角色", Rect2(30, 78, 298, 28), 14, MUTED)
	character_option = OptionButton.new()
	character_option.name = "CharacterOption"
	character_option.position = Vector2(30, 137)
	character_option.size = Vector2(294, 54)
	character_option.add_theme_font_override("font", UI_FONT)
	character_option.add_theme_font_size_override("font_size", 17)
	character_option.add_theme_color_override("font_color", TEXT)
	character_option.add_theme_stylebox_override("normal", _input_style(Color("#0b1718")))
	character_option.add_theme_stylebox_override("hover", _input_style(Color("#152b2c"), ACCENT))
	character_panel.add_child(character_option)
	var character_button := _button(character_panel, "进入游戏", Rect2(30, 224, 294, 56), Callable(self, "_on_character_selected"), PANEL_LIGHT, 17)
	character_button.add_theme_color_override("font_color", TEXT)
	character_status_label = _add_label(character_panel, "请选择人物", Rect2(30, 298, 294, 28), 13, MUTED)
	character_status_label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER


func _show_login() -> void:
	mode = "login"
	_clear_dynamic_map()
	map_loaded = false
	server_position_received = false
	server_floor = -1
	server_position = Vector2i.ZERO
	server_direction = 0
	player_cell = Vector2i.ZERO
	_clear_pending_move()
	_clear_walk_target()
	_clear_all_actors()
	pointer_held = false
	cursor_target_cell = Vector2i(-1, -1)
	if cursor_sprite != null:
		cursor_sprite.visible = false
	player_walking = false
	walk_frame_index = 0
	walk_frame_time = 0.0
	walk_animation_direction = -1
	player_object_index = -1
	map_window_floor = -1
	map_window_origin = Vector2i.ZERO
	map_window_size = Vector2i.ZERO
	map_window_name = ""
	map_window_key = ""
	map_checksum_key = ""
	map_pending_request_key = ""
	map_checksum_rect = Rect2i(-1, -1, 0, 0)
	npc_global = Vector2i(-1, -1)
	npc_cell = Vector2i(-1, -1)
	npc_name = ""
	character = ""
	character_names.clear()
	chat_lines.clear()
	pending_chat_messages.clear()
	chat_unread_count = 0
	map_sprite.visible = false
	player_sprite.visible = false
	npc_sprite.visible = false
	battle_sprite.visible = false
	battle_player_sprite.visible = false
	battle_enemy_sprite.visible = false
	world_controls.visible = false
	battle_controls.visible = false
	dialogue_panel.visible = false
	_set_chat_expanded(false)
	top_panel.visible = false
	character_controls.visible = false
	if character_option != null:
		character_option.clear()
	if character_status_label != null:
		character_status_label.text = "请选择人物"
	var actor_panel := ui.get_node_or_null("ActorPanel")
	if actor_panel != null:
		actor_panel.visible = false
	login_controls.visible = true
	login_status_label.text = "请输入账号和密码"
	login_status_label.add_theme_color_override("font_color", MUTED)
	login_status_label.visible = false


func _on_login_pressed() -> void:
	if network_phase not in ["offline", "error"]:
		login_status_label.visible = true
		login_status_label.text = "正在连接，请稍候…"
		return
	account = account_edit.text.strip_edges()
	password = password_edit.text
	if account.is_empty() or password.is_empty():
		login_status_label.visible = true
		login_status_label.text = "账号和密码不能为空"
		login_status_label.add_theme_color_override("font_color", DANGER)
		return
	var password_bytes := password.to_utf8_buffer()
	var password_valid := password_bytes.size() <= 12
	for character in password_bytes:
		if character < 0x21 or character > 0x7e:
			password_valid = false
			break
	if not password_valid:
		login_status_label.visible = true
		login_status_label.text = "密码须为 1–12 位半角英文字母、数字或符号，不支持空格"
		login_status_label.add_theme_color_override("font_color", DANGER)
		return
	login_status_label.visible = true
	login_status_label.text = "正在连接游戏网关…"
	login_status_label.add_theme_color_override("font_color", ACCENT)
	_network_connect()


func _network_connect() -> void:
	_close_socket()
	receive_buffer = PackedByteArray()
	next_message_id = 1
	network_phase = "connecting"
	var host := "10.0.2.2" if OS.has_feature("android") else "127.0.0.1"
	tcp = StreamPeerTCP.new()
	var result := tcp.connect_to_host(host, 9065)
	if result != OK:
		network_phase = "error"
		login_status_label.text = "连接失败：%s:9065" % host
		login_status_label.add_theme_color_override("font_color", DANGER)
		return
	_network_status("连接 %s:9065…" % host, ACCENT)


func _close_socket() -> void:
	if tcp != null:
		tcp.disconnect_from_host()
	tcp = null
	network_phase = "offline"


func _poll_network() -> void:
	if tcp == null:
		return
	tcp.poll()
	var status := tcp.get_status()
	if status == StreamPeerTCP.STATUS_ERROR or status == StreamPeerTCP.STATUS_NONE:
		if network_phase not in ["offline", "error"]:
			_network_error("网络连接已断开")
		return
	if status != StreamPeerTCP.STATUS_CONNECTED:
		return
	var available := tcp.get_available_bytes()
	if available <= 0:
		return
	var result := tcp.get_data(available)
	if result[0] != OK:
		_network_error("读取网络数据失败")
		return
	receive_buffer.append_array(result[1])
	if network_phase == "connecting":
		network_phase = "greeting"
	while true:
		if network_phase == "greeting":
			if receive_buffer.size() < 2:
				return
			if receive_buffer[0] != 76 or receive_buffer[1] != 0:
				_network_error("网关握手版本错误")
				return
			receive_buffer = receive_buffer.slice(2)
			network_phase = "account_wait"
			_send_named("ClientLogin", [Protocol.encode_string(account), Protocol.encode_string(password)])
			_network_status("已连接，正在验证账号…", ACCENT)
		if network_phase in ["greeting", "connecting"]:
			return
		var newline := _find_byte(receive_buffer, 10)
		if newline < 0:
			return
		var packet := receive_buffer.slice(0, newline + 1)
		receive_buffer = receive_buffer.slice(newline + 1)
		_handle_packet(packet)


func _find_byte(data: PackedByteArray, value: int) -> int:
	for index in range(data.size()):
		if data[index] == value:
			return index
	return -1


func _send_named(function_name: String, fields: Array[String]) -> void:
	if tcp == null or tcp.get_status() != StreamPeerTCP.STATUS_CONNECTED:
		return
	var raw := Protocol.raw_message(next_message_id, function_name, fields)
	next_message_id += 1
	var packet := Protocol.encode_packet(raw)
	var result := tcp.put_data(packet)
	if result != OK:
		_network_error("发送 %s 失败" % function_name)


func _handle_packet(packet: PackedByteArray) -> void:
	var raw := Protocol.decode_packet(packet)
	if raw.is_empty():
		_network_error("收到无法解码的服务器包")
		return
	var message := Protocol.parse_message(raw)
	if message.is_empty():
		_network_error("收到无法解析的服务器包")
		return
	var function_name: String = message.get("function", "")
	var fields: Array = message.get("fields", [])
	match function_name:
		"ClientLogin":
			_handle_account_response(fields)
		"CharList":
			_handle_character_list(fields)
		"CharLogin":
			_handle_character_login(fields)
		"XYD":
			_handle_xyd(fields)
		"CA":
			_handle_character_actions(fields)
		"C":
			_handle_characters(fields)
		"TK":
			_handle_talk(fields)
		"WN":
			_handle_window_notice(fields)
		"S":
			_handle_server_notice(fields)
		"MC":
			_handle_map_checksum(fields)
		"M":
			_handle_map_data(fields)
		"EN":
			_handle_encounter_response(fields)
		"B":
			_handle_battle_command(fields)
		"P":
			_handle_player_status(fields)
		_:
			# The legacy client has many optional status packets. Unknown packets
			# are ignored so an extra server feature cannot disconnect the phone.
			pass


func _handle_account_response(fields: Array) -> void:
	if fields.is_empty():
		_network_error("账号响应缺少结果")
		return
	var result := _decode_field_string(fields[0])
	if result != "ok":
		_network_error("账号或密码不正确：%s" % result)
		return
	network_phase = "character_list_wait"
	_network_status("账号验证成功，读取人物列表…", GOOD)
	_send_named("CharList", [])


func _handle_character_list(fields: Array) -> void:
	if fields.size() < 2:
		_network_error("人物列表响应不完整")
		return
	var result := _decode_field_string(fields[0])
	var data := _decode_field_string(fields[1])
	if result != "successful":
		_network_error("读取人物列表失败：%s" % result)
		return
	character_names.clear()
	# The legacy SAAC returns name|option pairs, repeated for each character.
	# Options are not needed until the character is entered; retain only the
	# names and let the player explicitly choose one, as in the PC client.
	var tokens := data.split("|", true)
	for index in range(0, tokens.size(), 2):
		var name := tokens[index].strip_edges()
		if not name.is_empty() and not character_names.has(name):
			character_names.append(name)
	if character_names.is_empty():
		_network_error("账号下没有可登入的人物")
		return
	character_option.clear()
	for name in character_names:
		character_option.add_item(name)
	character_option.select(0)
	character = character_names[0]
	network_phase = "character_select"
	login_controls.visible = false
	character_controls.visible = true
	character_status_label.text = "%d 个人物可登入" % character_names.size()
	character_status_label.add_theme_color_override("font_color", GOOD)
	_network_status("账号验证成功 · 等待选择人物", GOOD)


func _on_character_selected() -> void:
	if network_phase != "character_select":
		return
	var selected := character_option.selected
	if selected < 0 or selected >= character_names.size():
		character_status_label.text = "请先选择人物"
		character_status_label.add_theme_color_override("font_color", DANGER)
		return
	character = character_names[selected]
	network_phase = "character_login_wait"
	character_status_label.text = "正在登入 %s…" % character
	character_status_label.add_theme_color_override("font_color", ACCENT)
	_network_status("正在登入人物 %s…" % character, ACCENT)
	_send_named("CharLogin", [Protocol.encode_string(character)])


func _handle_character_login(fields: Array) -> void:
	if fields.size() < 2:
		_network_error("人物登入响应不完整")
		return
	var result := _decode_field_string(fields[0])
	var data := _decode_field_string(fields[1])
	if result != "successful":
		_network_error("人物登入失败：%s" % data)
		return
	network_phase = "world"
	mode = "world"
	_clear_dynamic_map()
	map_loaded = false
	server_position_received = false
	server_floor = -1
	map_window_floor = -1
	map_window_origin = Vector2i.ZERO
	map_window_size = Vector2i.ZERO
	map_window_key = ""
	map_checksum_key = ""
	map_pending_request_key = ""
	map_checksum_rect = Rect2i(-1, -1, 0, 0)
	server_position = Vector2i.ZERO
	server_direction = 0
	player_cell = Vector2i.ZERO
	_clear_pending_move()
	player_object_index = -1
	npc_global = Vector2i(-1, -1)
	npc_cell = Vector2i(-1, -1)
	npc_name = ""
	login_controls.visible = false
	character_controls.visible = false
	top_panel.visible = true
	var actor_node := ui.get_node_or_null("ActorPanel")
	if actor_node != null:
		actor_node.visible = true
	world_controls.visible = true
	map_sprite.visible = false
	player_sprite.visible = false
	npc_sprite.visible = false
	dialogue_panel.visible = true
	_set_chat_expanded(false)
	_network_status("已联网 · 服务器地图和坐标同步中", GOOD)
	event_label.text = "人物登入成功，等待服务器地图/角色数据…"


func _handle_xyd(fields: Array) -> void:
	if fields.size() < 3:
		return
	var x := _decode_field_int(fields[0])
	var y := _decode_field_int(fields[1])
	server_direction = _decode_field_int(fields[2])
	server_position = Vector2i(x, y)
	server_position_received = true
	if map_loaded:
		player_cell = _global_to_cell(server_position)
	_clear_pending_move()
	_set_player_texture(_has_walk_target())
	player_walking = _has_walk_target()
	_update_world_positions()
	location_label.text = "地图 %d  ·  坐标 %d,%d" % [server_floor, x, y]
	# XYD is an authoritative state update, not a player-facing notification.
	# Keep it in the HUD coordinate line instead of filling the play area with
	# one message for every movement step.
	event_label.text = ""
	_request_player_snapshot()
	_ensure_pc_map_window()
	_advance_walk_target()


func _handle_character_actions(fields: Array) -> void:
	# CA is the same authoritative character-action stream consumed by the PC
	# client. Record format: ``objindex|x|y|action|direction|...``. Apply the
	# player's own record to the authoritative server_position, and update any
	# other actor we already know about so nearby players/NPCs move between C
	# refreshes instead of teleporting.
	if fields.is_empty():
		return
	var data := _decode_field_string(fields[0])
	var moved_others := false
	for record in data.split(","):
		var parts := record.split("|")
		if parts.size() < 5:
			continue
		var object_index := _decode_base62(parts[0])
		var x := parts[1].to_int()
		var y := parts[2].to_int()
		var direction := parts[4].to_int()
		if player_object_index >= 0 and object_index == player_object_index:
			server_position = Vector2i(x, y)
			server_direction = direction
			server_position_received = true
			if map_loaded:
				player_cell = _global_to_cell(server_position)
			_clear_pending_move()
			_set_player_texture(_has_walk_target())
			player_walking = _has_walk_target()
			location_label.text = "地图 %d  ·  坐标 %d,%d" % [server_floor, x, y]
			event_label.text = ""
			_advance_walk_target()
			continue
		if actor_sprites.has(object_index):
			var state: Dictionary = actor_cells.get(object_index, {})
			var graphic := int(state.get("graphic", 0))
			var name := String(state.get("name", ""))
			_upsert_actor(object_index, Vector2i(x, y), direction, graphic, name)
			moved_others = true
	if server_position_received or moved_others:
		_update_world_positions()
	_ensure_pc_map_window()


func _handle_map_checksum(fields: Array) -> void:
	# MC is the same handshake used by the PC client: it describes the visible
	# rectangle and gives CRCs for its tile/object/event arrays. It does not carry
	# the arrays themselves; the client must ask for M.
	if fields.size() < 8:
		return
	var floor := _decode_field_int(fields[0])
	var x1 := _decode_field_int(fields[1])
	var y1 := _decode_field_int(fields[2])
	var x2 := _decode_field_int(fields[3])
	var y2 := _decode_field_int(fields[4])
	if floor < 0 or x2 <= x1 or y2 <= y1:
		return
	if floor != server_floor:
		_clear_server_actors_for_map_change()
		_clear_dynamic_map()
		map_window_key = ""
		map_checksum_key = ""
		map_pending_request_key = ""
		server_floor = floor
	map_checksum_rect = Rect2i(x1, y1, x2 - x1, y2 - y1)
	map_checksum_tiles = _decode_field_int(fields[5])
	map_checksum_objects = _decode_field_int(fields[6])
	map_checksum_events = _decode_field_int(fields[7])
	var map_name := _decode_field_string(fields[8]) if fields.size() >= 9 else ""
	map_checksum_key = _map_rect_key(floor, x1, y1, x2, y2)
	var requested_rect := _pc_map_rect()
	var requested_key := _map_rect_key(floor, requested_rect.position.x, requested_rect.position.y,
		requested_rect.end.x, requested_rect.end.y)
	if map_window_key == requested_key and map_loaded:
		_network_status("地图 %d · 服务器窗口校验通过" % floor, GOOD)
		return
	# Keep the already-rendered window visible while a one-cell movement causes
	# the next PC rectangle to arrive.  This avoids a blank flash on every step.
	_network_status("地图 %d · 正在从服务器读取 tile…" % floor, ACCENT)
	if not map_loaded:
		map_layers_root.visible = false
		map_sprite.visible = false
	_request_server_map(requested_rect, map_name)
	location_label.text = "地图 %d  · 坐标 %d,%d" % [floor, server_position.x, server_position.y]


func _handle_map_data(fields: Array) -> void:
	# M response: floor,x1,y1,x2,y2,data. The data string is
	# ``floorname|tiles|objects|events``; each layer is row-major and values are
	# base-62 integers (empty object/event cells are empty CSV tokens).
	if fields.size() < 6:
		return
	var floor := _decode_field_int(fields[0])
	var x1 := _decode_field_int(fields[1])
	var y1 := _decode_field_int(fields[2])
	var x2 := _decode_field_int(fields[3])
	var y2 := _decode_field_int(fields[4])
	if floor < 0 or x2 <= x1 or y2 <= y1:
		return
	var data := _decode_field_string(fields[5])
	var section := data.split("|", true)
	var expected := (x2 - x1) * (y2 - y1)
	if section.size() < 4:
		_network_error("服务器 M 地图数据分层不完整")
		return
	var tile_ids := _parse_map_id_layer(section[1], expected)
	var object_ids := _parse_map_id_layer(section[2], expected)
	var event_ids := _parse_map_id_layer(section[3], expected)
	if tile_ids.size() != expected or object_ids.size() != expected or event_ids.size() != expected:
		_network_error("服务器 M 地图数据数量不一致：需要 %d，收到 tile=%d object=%d event=%d" % [expected, tile_ids.size(), object_ids.size(), event_ids.size()])
		return
	var response_key := _map_rect_key(floor, x1, y1, x2, y2)
	if map_pending_request_key == response_key:
		map_pending_request_key = ""
	_render_server_map(floor, Vector2i(x1, y1), Vector2i(x2 - x1, y2 - y1), section[0], tile_ids, object_ids, event_ids)
	_ensure_pc_map_window()


func _request_server_map(rect: Rect2i, _map_name: String = "") -> void:
	if rect.size.x <= 0 or rect.size.y <= 0:
		return
	var key := _map_rect_key(server_floor, rect.position.x, rect.position.y, rect.end.x, rect.end.y)
	if map_pending_request_key == key:
		return
	map_pending_request_key = key
	_send_named("M", [
		Protocol.encode_int(server_floor),
		Protocol.encode_int(rect.position.x),
		Protocol.encode_int(rect.position.y),
		Protocol.encode_int(rect.end.x),
		Protocol.encode_int(rect.end.y),
	])


func _ensure_pc_map_window() -> void:
	if mode != "world" or server_floor < 0:
		return
	var rect := _pc_map_rect()
	if rect.size.x <= 0 or rect.size.y <= 0:
		return
	var key := _map_rect_key(server_floor, rect.position.x, rect.position.y,
		rect.end.x, rect.end.y)
	if map_loaded and map_window_key == key:
		return
	_request_server_map(rect, map_window_name)


func _pc_map_rect() -> Rect2i:
	# Before C/XYD arrives, fall back to the checksum rectangle so the first M
	# request is still valid.  As soon as the server gives us the authoritative
	# coordinate, switch to the exact 37x37 PC drawMap rectangle.
	if not server_position_received:
		return map_checksum_rect
	var x1 := server_position.x + PC_MAP_TILE_GRID_X1
	var y1 := server_position.y + PC_MAP_TILE_GRID_Y1
	var x2 := server_position.x + PC_MAP_TILE_GRID_X2
	var y2 := server_position.y + PC_MAP_TILE_GRID_Y2
	var floor_size := _floor_size(server_floor)
	if floor_size.x > 0 and floor_size.y > 0:
		x1 = clampi(x1, 0, floor_size.x)
		y1 = clampi(y1, 0, floor_size.y)
		x2 = clampi(x2, 0, floor_size.x)
		y2 = clampi(y2, 0, floor_size.y)
	return Rect2i(x1, y1, maxi(0, x2 - x1), maxi(0, y2 - y1))


func _floor_size(floor: int) -> Vector2i:
	var key := str(floor)
	var info: Dictionary = map_manifest.get(key, {})
	if info.is_empty():
		info = resource_map_manifest.get(key, {})
	if info.is_empty():
		return Vector2i(-1, -1)
	var width := int(info.get("source_width", info.get("width", 0)))
	var height := int(info.get("source_height", info.get("height", 0)))
	return Vector2i(width, height) if width > 0 and height > 0 else Vector2i(-1, -1)


func _parse_map_id_layer(encoded: String, expected: int) -> Array:
	var result: Array = []
	if encoded.is_empty():
		return result
	var tokens := encoded.split(",", true)
	for token in tokens:
		if result.size() >= expected:
			break
		if token.is_empty():
			result.append(0)
		else:
			result.append(_decode_base62(token))
	return result


func _map_rect_key(floor: int, x1: int, y1: int, x2: int, y2: int) -> String:
	return "%d:%d:%d:%d:%d" % [floor, x1, y1, x2, y2]


func _handle_map_packet_legacy(function_name: String, fields: Array) -> void:
	# MC carries the authoritative floor and checksums; M is the older full
	# rectangle form.  The phone still renders the extracted PC DAT for that
	# floor, but never chooses a local floor before this packet arrives.
	if fields.is_empty():
		return
	var floor := _decode_field_int(fields[0])
	if floor < 0:
		return
	if floor != server_floor:
		_clear_server_actors_for_map_change()
	server_floor = floor
	if not _activate_map_floor(floor):
		map_loaded = false
		map_sprite.visible = false
		player_sprite.visible = false
		_network_status("服务器地图 %d 尚未随包提供", DANGER)
		event_label.text = "服务器进入地图 %d，但本机尚未提取该地图贴图。" % floor
		return
	map_loaded = true
	map_sprite.visible = mode == "world"
	_network_status("地图 %d · 与服务器地图坐标同步" % server_floor, GOOD)
	if server_position_received:
		player_cell = _global_to_cell(server_position)
		player_sprite.visible = true
		_update_world_positions()
	else:
		_update_map_object_depth()
	location_label.text = "地图 %d  ·  坐标 %d,%d" % [server_floor, server_position.x, server_position.y]
	event_label.text = "服务器地图已确认，等待角色同步…" if not server_position_received else "服务器地图和坐标已同步"


func _handle_characters(fields: Array) -> void:
	if fields.is_empty():
		return
	var data := _decode_field_string(fields[0])
	actor_count = 0
	var seen_actors: Dictionary = {}
	var found_npc := false
	for record in data.split(","):
		var parts := record.split("|")
		if parts.size() < 4 or parts[0].is_empty():
			continue
		if parts.size() >= 12:
			# Character C records start with `type|object-index|x|y|...`;
			# the object index is the second token (CA records use it as the
			# first token, so do not share that offset here).
			var object_index := _decode_base62(parts[1])
			var name := parts[8]
			var x := parts[2].to_int()
			var y := parts[3].to_int()
			var direction := parts[4].to_int()
			var image_number := parts[5].to_int()
			actor_count += 1
			if name == character or parts[9] == character:
				var had_pending_move := pending_move
				var move_origin := pending_move_origin
				var received_position := Vector2i(x, y)
				player_object_index = object_index
				server_position = received_position
				server_position_received = true
				if map_loaded:
					player_cell = _global_to_cell(server_position)
				server_direction = direction
				player_graphic_id = image_number
				_clear_pending_move()
				var blocked := had_pending_move and received_position == move_origin
				if blocked:
					_clear_walk_target()
				player_walking = _has_walk_target()
				_set_player_texture(player_walking)
				player_sprite.visible = map_loaded
				location_label.text = "地图 %d  ·  坐标 %d,%d" % [server_floor, x, y]
				if blocked:
					event_label.text = "前方不可通行"
				else:
					event_label.text = ""
				_advance_walk_target()
			else:
				_upsert_actor(object_index, Vector2i(x, y), direction, image_number, name)
				seen_actors[object_index] = true
				if not found_npc:
					# The C packet is authoritative for nearby NPC coordinates. Use
					# the first non-player actor as the tap-to-talk pointer; talk
					# range is enforced when the player actually taps it.
					npc_global = Vector2i(x, y)
					npc_cell = _global_to_cell(npc_global)
					npc_name = name
					found_npc = true
		if parts.size() >= 6 and parts.size() < 12:
			# Item/money records are intentionally not drawn in this first
			# portrait slice, but still count as server actors in the HUD.
			actor_count += 1
	_prune_actors_not_in(seen_actors)
	# The original single npc_sprite is now redundant — every non-player actor
	# gets its own sprite via _upsert_actor. Keep the field cleared so it never
	# ghosts on top of the real actor.
	npc_sprite.visible = false
	if not found_npc:
		npc_global = Vector2i(-1, -1)
		npc_cell = Vector2i(-1, -1)
		npc_name = ""
	_update_world_positions()
	if found_npc:
		actor_label.text = "服务器角色 %d · 最近 NPC %s（点它对话）" % [actor_count, npc_name if not npc_name.is_empty() else "附近"]
	else:
		actor_label.text = "服务器角色 %d · 暂无可对话 NPC" % actor_count


func _upsert_actor(object_index: int, global_pos: Vector2i, direction: int, graphic_id: int, name: String) -> void:
	var sprite: Sprite2D = actor_sprites.get(object_index, null)
	if sprite == null:
		sprite = Sprite2D.new()
		sprite.name = "Actor%d" % object_index
		add_child(sprite)
		actor_sprites[object_index] = sprite
	var texture := _actor_idle_texture(graphic_id, direction)
	if texture == null:
		texture = NPC_TEXTURE
	sprite.texture = texture
	actor_cells[object_index] = {
		"cell": _global_to_cell(global_pos),
		"global": global_pos,
		"direction": direction,
		"graphic": graphic_id,
		"name": name,
	}
	sprite.visible = map_loaded


func _prune_actors_not_in(seen: Dictionary) -> void:
	var to_remove: Array = []
	for key in actor_sprites.keys():
		if not seen.has(key):
			to_remove.append(key)
	for key in to_remove:
		var sprite: Sprite2D = actor_sprites[key]
		if is_instance_valid(sprite):
			sprite.queue_free()
		actor_sprites.erase(key)
		actor_cells.erase(key)


func _clear_all_actors() -> void:
	for key in actor_sprites.keys():
		var sprite: Sprite2D = actor_sprites[key]
		if is_instance_valid(sprite):
			sprite.queue_free()
	actor_sprites.clear()
	actor_cells.clear()


func _handle_talk(fields: Array) -> void:
	if fields.size() < 2:
		return
	var message := _decode_field_string(fields[1])
	if message.begins_with("P|"):
		message = message.substr(2)
	# The server echoes a local chat message through TK.  Avoid showing the
	# same line twice after the phone has already placed it in the chat history.
	if pending_chat_messages.has(message):
		pending_chat_messages.erase(message)
		return
	# Server welcome/public chat should not force the portrait UI open.  It is
	# retained in the same history and appears as unread count on the compact
	# bar until the player chooses to expand it.
	_append_chat_line("聊天", message, false)


func _handle_window_notice(fields: Array) -> void:
	# WN is the PC client's general-purpose window/notice stream.  Many WN
	# packets open menus that do not belong in a compact phone chat.  The PC
	# LOGIN.H enum reserves 0/1/10/11 for message and wide-message windows;
	# those carry useful NPC/system text and remain visible here.
	if fields.size() < 5:
		return
	var window_type := _decode_field_int(fields[0])
	if window_type not in WN_MESSAGE_TYPES:
		return
	var message := _decode_field_string(fields[4])
	if message.begins_with("P|"):
		message = message.substr(2)
	_append_chat_line("系统", message, false)


func _handle_server_notice(fields: Array) -> void:
	# S contains packed status strings (inventory, character flags, etc.) rather
	# than player-facing chat.  Do not dump those opaque records into the chat
	# box; meaningful text arrives through TK/WN just as it does on PC.
	if fields.is_empty():
		return


func _handle_encounter_response(fields: Array) -> void:
	# EN here is the server -> client encounter notification.  The PC server
	# emits it from the successful-walk encounter roll; the phone never sends
	# an EN packet or exposes a manual "encounter" action.
	if fields.size() < 2:
		return
	var result := _decode_field_int(fields[0])
	var field := _decode_field_int(fields[1])
	if result <= 0:
		_show_dialogue("服务器未创建这次随机战斗（result=%d）。" % result)
		return
	_enter_battle(field, "服务器随机遇敌（result=%d）" % result)


func _handle_battle_command(fields: Array) -> void:
	if fields.is_empty():
		return
	var command := _decode_field_string(fields[0])
	if command.begins_with("BP|"):
		var parts := command.split("|")
		if parts.size() >= 4:
			battle_my_bid = _hex_int(parts[1])
			battle_status_label.text = "我方编号 %X · 服务器等待指令" % battle_my_bid
	elif command.begins_with("BC|"):
		_parse_battle_status(command)
	elif command.begins_with("BD|"):
		battle_log.append("服务器结算：%s" % command)
	elif command.begins_with("BA|"):
		battle_status_label.text = "服务器回合更新"
	elif command.begins_with("BE|") or command.begins_with("BX|"):
		battle_log.append("战斗事件：%s" % command)
	elif command.begins_with("BK|"):
		battle_log.append(command.substr(3))
	else:
		battle_log.append("服务器：%s" % command)
	_refresh_battle_log()


func _handle_player_status(_fields: Array) -> void:
	# P is a packed legacy status string. It is deliberately ignored here: it
	# is an internal heartbeat/inventory update, not a chat or notification.
	# Guessing offsets also makes the phone UI flash on every status tick.
	pass


func _parse_battle_status(command: String) -> void:
	var parts := command.split("|")
	if parts.size() < 3:
		return
	var records := parts.slice(2)
	var record_count := int(records.size() / 8)
	var first_enemy := -1
	for index in range(record_count):
		var start := index * 8
		if start + 7 >= records.size():
			break
		var bid := _hex_int(records[start])
		var name := records[start + 1]
		var hp := _hex_int(records[start + 5])
		var max_hp := _hex_int(records[start + 6])
		var flags := _hex_int(records[start + 7])
		if name == character:
			battle_my_bid = bid
		elif first_enemy < 0 and (flags & 0x01) == 0:
			first_enemy = bid
			battle_enemy_hp = hp
			battle_enemy_max_hp = max_hp
	if first_enemy >= 0:
		battle_target_bid = first_enemy
	var hp_text := "未知"
	if battle_enemy_hp >= 0:
		hp_text = "%d/%d" % [battle_enemy_hp, battle_enemy_max_hp]
	battle_status_label.text = "我方 %X · 敌方 %X · 敌 HP %s" % [battle_my_bid, battle_target_bid, hp_text]


# World-space delta vectors indexed by PC direction (0=N .. 7=NW, going CW
# through E/S/W). Kept next to _direction_letter so tap-to-move, continuous
# walking and _on_move all agree on which letter matches which step.
const DIRECTION_DELTAS := [
	Vector2i(0, -1), Vector2i(1, -1), Vector2i(1, 0), Vector2i(1, 1),
	Vector2i(0, 1), Vector2i(-1, 1), Vector2i(-1, 0), Vector2i(-1, -1),
]


func _direction_letter(index: int) -> String:
	return String.chr("a".unicode_at(0) + clampi(index, 0, 7))


func _direction_index_for(delta: Vector2i) -> int:
	for index in range(DIRECTION_DELTAS.size()):
		if DIRECTION_DELTAS[index] == delta:
			return index
	# Fallback: snap arbitrary delta to the nearest of 8 world compass directions
	# using atan2. World +y is south, so the raw rounded quadrant is offset by 2
	# to align with the PC 0=N convention.
	if delta == Vector2i.ZERO:
		return -1
	var angle := atan2(float(delta.y), float(delta.x))
	var raw := int(round(angle / (PI / 4.0)))
	return ((raw + 2) % 8 + 8) % 8


func _clear_walk_target() -> void:
	walk_target = Vector2i(-1, -1)


func _has_walk_target() -> bool:
	return walk_target.x >= 0


func _on_move(delta: Vector2i) -> void:
	# Kept for the offline walk smoke test and any code that still wants to
	# fire a single-step move by world delta.
	_clear_walk_target()
	var index := _direction_index_for(delta)
	if index < 0:
		return
	_send_step(index)


func _send_step(direction_index: int) -> void:
	if mode != "world" or not map_loaded or not server_position_received or pending_move:
		return
	if direction_index < 0 or direction_index >= DIRECTION_DELTAS.size():
		return
	var start_position := server_position
	var delta: Vector2i = DIRECTION_DELTAS[direction_index]
	var target_position := start_position + delta
	var direction_letter := _direction_letter(direction_index)
	pending_move = true
	pending_move_started_ms = Time.get_ticks_msec()
	pending_move_origin = start_position
	pending_move_target = target_position
	pending_move_direction = direction_letter
	pending_move_snapshot_retries = 0
	pending_move_last_snapshot_ms = pending_move_started_ms
	server_direction = direction_index
	player_walking = true
	_set_player_texture(true)
	event_label.text = ""
	# W follows the PC protocol: x/y are the current server tile and the
	# direction string describes one or more steps from that tile. Do not
	# predict the destination locally: the server validates walkability and
	# XYD/CA are the only authority for the resulting coordinate.
	_send_named("W", [
		Protocol.encode_int(start_position.x),
		Protocol.encode_int(start_position.y),
		Protocol.encode_string(direction_letter),
	])
	# The server's normal response to W is MC/CA, not necessarily XYD. C is
	# the same authoritative object snapshot requested by the PC client when
	# it needs to rebuild an actor, and TCP ordering guarantees it is processed
	# after this W request.
	_request_player_snapshot()


func _advance_walk_target() -> void:
	# While the pointer is held, PC behaviour is to re-project the cursor's screen
	# position each step so the character heads toward wherever the finger is
	# right now, not the tile it first pressed on.
	if pointer_held and not pending_move and map_loaded and server_position_received:
		var live_cell := _screen_to_cell(pointer_last_screen)
		cursor_target_cell = live_cell
		if live_cell == player_cell:
			_clear_walk_target()
		else:
			walk_target = live_cell + map_crop_origin
	if not _has_walk_target() or pending_move:
		return
	if mode != "world" or not map_loaded or not server_position_received:
		_clear_walk_target()
		return
	if server_position == walk_target:
		_clear_walk_target()
		player_walking = false
		_set_player_texture(false)
		return
	var delta := walk_target - server_position
	# Move one step at a time along the dominant axis first; if both axes are
	# non-zero, pick the 8-direction that best matches the remaining vector so
	# diagonal targets take diagonal steps instead of an L-shaped path.
	var step_dx := clampi(delta.x, -1, 1)
	var step_dy := clampi(delta.y, -1, 1)
	var step := Vector2i(step_dx, step_dy)
	var index := _direction_index_for(step)
	if index < 0:
		_clear_walk_target()
		return
	_send_step(index)


func _make_cursor_sprite() -> Node2D:
	# Yellow diamond outline that traces the isometric footprint of one tile.
	# Used so the player can see which cell a tap/hold resolves to before the
	# server confirms the step, matching the PC client's mouse-tile highlight.
	var root := Node2D.new()
	root.name = "TapCursor"
	root.z_index = 25
	root.visible = false
	var outline := Line2D.new()
	outline.name = "Outline"
	outline.width = 2.0
	outline.default_color = ACCENT
	outline.closed = true
	outline.points = PackedVector2Array([
		Vector2(0, -TILE_STEP.y),
		Vector2(TILE_STEP.x, 0),
		Vector2(0, TILE_STEP.y),
		Vector2(-TILE_STEP.x, 0),
	])
	root.add_child(outline)
	return root


func _screen_to_cell(screen_pos: Vector2) -> Vector2i:
	var canvas_inverse := get_canvas_transform().affine_inverse()
	var world_pos := canvas_inverse * screen_pos
	var local := world_pos - map_render_origin
	# Inverse of _cell_to_world: local.x = (cx+cy)*32, local.y = (cy-cx)*24.
	var u := local.x / TILE_STEP.x
	var v := local.y / TILE_STEP.y
	return Vector2i(int(round((u - v) * 0.5)), int(round((u + v) * 0.5)))


func _refresh_cursor() -> void:
	if cursor_sprite == null:
		return
	# The PC client hides the cursor while a hold-walk is active. We mirror that
	# so the player sees the target on the initial tap but the cursor gets out
	# of the way once the character actually starts moving toward it.
	var should_show := pointer_held and cursor_target_cell.x >= 0 and not player_walking
	if not should_show:
		cursor_sprite.visible = false
		return
	cursor_sprite.position = _cell_to_world(cursor_target_cell, map_render_origin)
	cursor_sprite.visible = true


func _set_pointer_target(screen_pos: Vector2) -> void:
	pointer_last_screen = screen_pos
	if mode != "world" or not map_loaded or not server_position_received:
		return
	var cell := _screen_to_cell(screen_pos)
	cursor_target_cell = cell
	_refresh_cursor()
	_handle_world_tap(cell)


func _release_pointer() -> void:
	pointer_held = false
	cursor_target_cell = Vector2i(-1, -1)
	_clear_walk_target()
	_refresh_cursor()


func _handle_world_tap(tap_cell: Vector2i) -> void:
	if mode != "world" or not map_loaded or not server_position_received:
		return
	if tap_cell == player_cell:
		_clear_walk_target()
		return
	var tap_global := tap_cell + map_crop_origin
	# Any visible actor on this cell becomes the talk target. Retarget npc_global
	# so _on_talk sends TK to whoever the player pointed at, not just the first
	# NPC the last C packet listed.
	for key in actor_cells.keys():
		var state: Dictionary = actor_cells[key]
		if state.is_empty():
			continue
		var actor_global: Vector2i = state["global"]
		if actor_global == tap_global:
			npc_global = actor_global
			npc_cell = state["cell"]
			npc_name = state.get("name", "")
			var distance: int = int(abs(server_position.x - actor_global.x) + abs(server_position.y - actor_global.y))
			if distance <= 3:
				_clear_walk_target()
				_on_talk()
				return
			# Out of talk range: walk toward the actor; block detection stops
			# one cell short of a solid actor tile.
			walk_target = actor_global
			_advance_walk_target()
			return
	walk_target = tap_global
	_advance_walk_target()


func _unhandled_input(event: InputEvent) -> void:
	if mode != "world":
		return
	if event is InputEventScreenTouch:
		if event.pressed:
			pointer_held = true
			_set_pointer_target(event.position)
		else:
			_release_pointer()
		get_viewport().set_input_as_handled()
		return
	if event is InputEventScreenDrag:
		if pointer_held:
			_set_pointer_target(event.position)
			get_viewport().set_input_as_handled()
		return
	if event is InputEventMouseButton:
		if event.button_index != MOUSE_BUTTON_LEFT:
			return
		if event.pressed:
			pointer_held = true
			_set_pointer_target(event.position)
		else:
			_release_pointer()
		get_viewport().set_input_as_handled()
		return
	if event is InputEventMouseMotion:
		if pointer_held and (event.button_mask & MOUSE_BUTTON_MASK_LEFT) != 0:
			_set_pointer_target(event.position)
			get_viewport().set_input_as_handled()
		return


func _on_talk() -> void:
	if mode != "world" or not map_loaded or not server_position_received or npc_global.x < 0:
		return
	var distance: int = int(abs(server_position.x - npc_global.x) + abs(server_position.y - npc_global.y))
	if distance > 3:
		_show_dialogue("NPC 在服务器坐标 %d,%d，先走近再对话。" % [npc_global.x, npc_global.y])
		return
	_send_named("TK", [
		Protocol.encode_int(npc_global.x),
		Protocol.encode_int(npc_global.y),
		Protocol.encode_string("你好"),
		Protocol.encode_int(0),
		Protocol.encode_int(3),
	])
	_show_dialogue("已向 %s 发送对话，请等服务器回应…" % (npc_name if not npc_name.is_empty() else "NPC"))


func _enter_battle(field: int, message: String) -> void:
	mode = "battle"
	_clear_walk_target()
	pointer_held = false
	cursor_target_cell = Vector2i(-1, -1)
	if cursor_sprite != null:
		cursor_sprite.visible = false
	player_walking = false
	battle_log = [message]
	battle_enemy_hp = -1
	battle_enemy_max_hp = -1
	battle_target_bid = 10
	map_sprite.visible = false
	player_sprite.visible = false
	npc_sprite.visible = false
	top_panel.visible = false
	var actor_node := ui.get_node_or_null("ActorPanel")
	if actor_node != null:
		actor_node.visible = false
	world_controls.visible = false
	battle_controls.visible = true
	battle_sprite.visible = true
	battle_player_sprite.visible = true
	battle_enemy_sprite.visible = true
	_set_battle_player_texture()
	# The battle map is the extracted PC battleMap/00 asset. Actors use real
	# sprite frames but remain in a portrait-friendly central pair of cells.
	battle_player_sprite.position = _cell_to_world(Vector2i(7, 13), BATTLE_RENDER_ORIGIN)
	battle_enemy_sprite.position = _cell_to_world(Vector2i(13, 7), BATTLE_RENDER_ORIGIN)
	if field >= 0:
		_network_status("服务器战斗地图 %d", GOOD)
	_refresh_battle_log()


func _on_attack() -> void:
	if mode != "battle":
		return
	_send_named("B", [Protocol.encode_string("H|%X" % battle_target_bid)])
	battle_log.append("已发送攻击 H|%X" % battle_target_bid)
	_refresh_battle_log()


func _on_guard() -> void:
	if mode != "battle":
		return
	_send_named("B", [Protocol.encode_string("G")])
	battle_log.append("已发送防御 G")
	_refresh_battle_log()


func _on_escape() -> void:
	if mode != "battle":
		return
	_send_named("B", [Protocol.encode_string("E")])
	battle_log.append("已发送逃跑 E，等待服务器结果…")
	_refresh_battle_log()


func _refresh_battle_log() -> void:
	if battle_log.size() > 4:
		battle_log = battle_log.slice(battle_log.size() - 4)
	battle_log_label.text = "\n".join(battle_log)


func _show_dialogue(message: String) -> void:
	_append_chat_line("系统", message, true)


func _toggle_chat() -> void:
	_set_chat_expanded(not chat_expanded)


func _set_chat_expanded(expanded: bool) -> void:
	chat_expanded = expanded
	if expanded:
		chat_unread_count = 0
	if dialogue_panel == null:
		return
	var height := 210.0 if expanded else 48.0
	dialogue_panel.size.y = height
	_center_bottom(dialogue_panel, 366, height, 0)
	if dialogue_title_label != null:
		if chat_unread_count > 0:
			dialogue_title_label.text = "聊天 · %d 条未读" % chat_unread_count
		else:
			dialogue_title_label.text = "聊天"
	if chat_toggle_button != null:
		chat_toggle_button.text = "收起" if expanded else "展开"
	if dialogue_label != null:
		dialogue_label.visible = expanded
	if chat_input != null:
		chat_input.visible = expanded
	if chat_send_button != null:
		chat_send_button.visible = expanded


func _close_dialogue() -> void:
	_set_chat_expanded(false)


func _on_chat_submitted(_value: String) -> void:
	_on_chat_send()


func _on_chat_send() -> void:
	if mode != "world" or not map_loaded or not server_position_received or chat_input == null:
		return
	var message := chat_input.text.strip_edges()
	if message.is_empty():
		return
	if message.length() > 120:
		message = message.substr(0, 120)
	pending_chat_messages.append(message)
	_send_named("TK", [
		Protocol.encode_int(server_position.x),
		Protocol.encode_int(server_position.y),
		Protocol.encode_string(message),
		Protocol.encode_int(0),
		Protocol.encode_int(3),
	])
	_append_chat_line("我", message, false)
	chat_input.clear()
	# Android's IME covers the bottom controls while a LineEdit owns focus.
	# Releasing focus after sending hides the IME and returns the panel to its
	# normal bottom anchor; the next tap can open it again.
	chat_input.release_focus()


func _append_chat_line(speaker: String, message: String, expand: bool) -> void:
	var clean := _clean_chat_message(message)
	if clean.is_empty() or _is_connection_notice(clean):
		return
	var line := clean if speaker.is_empty() else "%s：%s" % [speaker, clean]
	chat_lines.append(line)
	if chat_lines.size() > 32:
		chat_lines = chat_lines.slice(chat_lines.size() - 32)
	if not chat_expanded:
		chat_unread_count += 1
	if dialogue_label != null:
		var visible_lines := chat_lines
		if visible_lines.size() > 7:
			visible_lines = visible_lines.slice(visible_lines.size() - 7)
		dialogue_label.text = "\n".join(visible_lines)
	if dialogue_panel != null and mode != "login":
		dialogue_panel.visible = true
		if expand:
			_set_chat_expanded(true)
		else:
			_set_chat_expanded(chat_expanded)


func _clean_chat_message(message: String) -> String:
	var clean := message.strip_edges()
	if clean.begins_with("P|"):
		clean = clean.substr(2).strip_edges()
	return clean


func _is_connection_notice(message: String) -> bool:
	var text := message.to_lower().replace(" ", "").replace("　", "")
	# Connection handshakes are transport state, not chat. Filter only short
	# connection-only notices so a real sentence mentioning a server is kept.
	# The legacy server prefixes its handshake with a product name, so the
	# phrase check must not depend on the total line length.
	if text.contains("服务器已连接") or text.contains("伺服器已連線"):
		return true
	if text.length() <= 24 and text.contains("服务器") and text.contains("连接"):
		return true
	if text.length() <= 24 and text.contains("伺服器") and text.contains("連線"):
		return true
	return text in ["连接成功", "連線成功", "网络连接已建立", "網路連線已建立"]


func _update_world_positions() -> void:
	if not map_loaded:
		return
	if server_position_received:
		player_sprite.position = _cell_to_world(player_cell, map_render_origin) + Vector2(0, -26)
		player_sprite.z_index = _actor_z_index(player_cell)
	for key in actor_sprites.keys():
		var sprite: Sprite2D = actor_sprites[key]
		if not is_instance_valid(sprite):
			continue
		var state: Dictionary = actor_cells.get(key, {})
		if state.is_empty():
			continue
		var cell: Vector2i = state["cell"]
		sprite.position = _cell_to_world(cell, map_render_origin) + Vector2(0, -26)
		sprite.z_index = _actor_z_index(cell)
		sprite.visible = map_loaded
	_update_map_object_depth()
	if camera != null and mode == "world" and server_position_received:
		camera.position = _cell_to_world(player_cell, map_render_origin)


func _actor_z_index(cell: Vector2i) -> int:
	# Characters share the PC's parts priority, but must be ordered by the same
	# diagonal traversal as map objects.  A fixed z=20 puts the player beneath
	# every object in a server window (and made the avatar disappear on forest
	# maps); this keeps same-cell objects behind the actor while retaining depth
	# against objects on neighbouring cells.
	if map_window_size.x <= 0 or map_window_size.y <= 0:
		return 2
	var local_x := clampi(cell.x, 0, map_window_size.x - 1)
	var local_y := clampi(cell.y, 0, map_window_size.y - 1)
	var index := local_y * map_window_size.x + local_x
	var draw_order := _pc_map_draw_order(map_window_size)
	var count := draw_order.size()
	return count + 1 + (count - int(draw_order[index])) + 1


func _update_map_object_depth() -> void:
	# PC inserts map parts and characters into one priority list.  A fixed
	# "all actors above all objects" z-index makes a tree in the southeast
	# quadrant appear behind the player, unlike the PC renderer.  Keep the
	# server's diagonal submission order, then move only objects that the PC's
	# default priority rule would place in front of an actor.
	if map_object_layer == null or map_window_size.x <= 0:
		return
	var actors: Array[Vector2i] = []
	if server_position_received:
		actors.append(server_position)
	for key in actor_cells.keys():
		var state: Dictionary = actor_cells[key]
		if state.is_empty():
			continue
		var global_pos: Vector2i = state["global"]
		if global_pos.x >= 0:
			actors.append(global_pos)
	var count := map_window_size.x * map_window_size.y
	for child in map_object_layer.get_children():
		if not child is Sprite2D or not child.has_meta("map_global"):
			continue
		var global_cell: Vector2i = child.get_meta("map_global")
		var order := int(child.get_meta("map_order", 0))
		var z := (count + 1) + (count - order)
		for actor in actors:
			if _pc_default_part_over_actor(global_cell, actor):
				z = max(z, _actor_z_index(_global_to_cell(actor)) + 2)
		child.z_index = z


func _pc_default_part_over_actor(part_global: Vector2i, actor_global: Vector2i) -> bool:
	# This is the default branch of checkPrioPartsVsChar() in the preserved
	# MAP.CPP.  The special priority/hit types are uncommon in the compact
	# portrait window; the diagonal relation below covers ordinary trees,
	# buildings and map decorations while preserving same-cell actor priority.
	if part_global == actor_global:
		return false
	return part_global.x <= actor_global.x and part_global.y >= actor_global.y


func _clear_dynamic_map() -> void:
	if map_tile_layer != null:
		for child in map_tile_layer.get_children():
			child.free()
	if map_object_layer != null:
		for child in map_object_layer.get_children():
			child.free()
	if map_layers_root != null:
		map_layers_root.visible = false
	map_window_tile_ids = []
	map_window_object_ids = []
	map_window_event_ids = []
	map_window_missing_bitmaps = 0


func _render_server_map(
	floor: int,
	origin: Vector2i,
	size: Vector2i,
	map_name: String,
	tile_ids: Array,
	object_ids: Array,
	event_ids: Array,
) -> void:
	if floor != server_floor or size.x <= 0 or size.y <= 0:
		return
	var expected := size.x * size.y
	if tile_ids.size() != expected or object_ids.size() != expected or event_ids.size() != expected:
		return
	_clear_dynamic_map()
	map_window_floor = floor
	map_window_origin = origin
	map_window_size = size
	map_window_name = map_name
	map_window_tile_ids = tile_ids.duplicate()
	map_window_object_ids = object_ids.duplicate()
	map_window_event_ids = event_ids.duplicate()
	map_crop_origin = origin
	map_crop_size = size
	# PC's camMapToGamen() uses screen_x=(x+y)*32 and screen_y=(y-x)*24.
	# Keep the server rectangle local, but use the same orientation and put the
	# minimum y (the right edge) inside a positive canvas.
	map_render_origin = Vector2(256.0, float((size.x - 1) * int(TILE_STEP.y) + 256))
	map_window_key = _map_rect_key(floor, origin.x, origin.y, origin.x + size.x, origin.y + size.y)
	var draw_order := _pc_map_draw_order(size)
	_render_server_map_layer(tile_ids, map_tile_layer, 0, draw_order)
	_render_server_map_layer(object_ids, map_object_layer, 1, draw_order)
	map_loaded = true
	map_layers_root.visible = mode == "world"
	map_sprite.visible = false
	if server_position_received:
		player_cell = _global_to_cell(server_position)
		player_sprite.visible = true
		_update_world_positions()
	if map_window_missing_bitmaps > 0:
		_network_status("地图 %d · tile %d×%d · 缺贴图 %d" % [floor, size.x, size.y, map_window_missing_bitmaps], DANGER)
	else:
		_network_status("地图 %d · 服务器 tile %d×%d 已同步" % [floor, size.x, size.y], GOOD)
	# The tile counts are already visible in the network HUD. Do not turn this
	# internal map bookkeeping into a persistent banner over the play field.
	event_label.text = ""
	location_label.text = "地图 %d  · 坐标 %d,%d" % [floor, server_position.x, server_position.y]


func _render_server_map_layer(ids: Array, parent: Node2D, layer_order: int, draw_order: Array) -> void:
	if parent == null:
		return
	for index in range(ids.size()):
		var bitmap_id: int = int(ids[index])
		# The PC renderer treats 0..99 as invisible/special map controls. They
		# affect collision, sound or events but are never submitted as sprites.
		if bitmap_id <= CG_INVISIBLE:
			continue
		var row := int(index / map_window_size.x)
		var column := index % map_window_size.x
		var info := _bitmap_info(bitmap_id)
		var texture := _texture_for_bitmap(bitmap_id)
		if texture == null or info.is_empty():
			map_window_missing_bitmaps += 1
			continue
		var sprite := Sprite2D.new()
		sprite.name = "Bitmap%d_%d_%d" % [bitmap_id, column, row]
		sprite.texture = texture
		sprite.centered = false
		var anchor := map_render_origin + Vector2(
			float((column + row) * int(TILE_STEP.x)),
			float((row - column) * int(TILE_STEP.y)),
		)
		sprite.position = anchor + Vector2(float(info.get("xoffset", 0)), float(info.get("yoffset", 0)))
		# PC submits parts in the diagonal traversal below and the display
		# sorter draws later submissions first for equal priority. Godot's larger
		# z is on top, so invert that order. The layer stride keeps every parts
		# sprite above every ground tile, matching DISP_PRIO_PARTS > TILE.
		var order := int(draw_order[index])
		var count := draw_order.size()
		sprite.z_index = layer_order * (count + 1) + (count - order)
		if layer_order == 1:
			sprite.set_meta("map_global", map_window_origin + Vector2i(column, row))
			sprite.set_meta("map_order", order)
		parent.add_child(sprite)


func _pc_map_draw_order(size: Vector2i) -> Array:
	# Exact traversal from the preserved PC drawMap() implementation:
	# ti=height-1,tj=0; walk (i--,j--) and then advance tj along the right
	# edge until it is exhausted, finally decrementing ti.
	var result: Array = []
	result.resize(size.x * size.y)
	var order := 0
	var ti := size.y - 1
	var tj := 0
	while ti >= 0:
		var row := ti
		var column := tj
		while row >= 0 and column >= 0:
			result[row * size.x + column] = order
			order += 1
			row -= 1
			column -= 1
		if tj < size.x - 1:
			tj += 1
		else:
			ti -= 1
	return result


func _load_asset_manifest() -> void:
	var file := FileAccess.open(ASSET_MANIFEST_PATH, FileAccess.READ)
	if file == null:
		return
	var parsed = JSON.parse_string(file.get_as_text())
	if not parsed is Dictionary:
		return
	bitmap_manifest = parsed.get("bitmaps", {})
	map_manifest = parsed.get("maps", {})
	resource_map_manifest = parsed.get("resource_maps", {})
	if map_manifest.is_empty() and parsed.has("map"):
		var old_map: Dictionary = parsed.get("map", {})
		if not old_map.is_empty() and old_map.has("id"):
			map_manifest[str(old_map["id"])] = old_map
	bitmap_aliases = {}
	var explicit_aliases = parsed.get("bitmap_aliases", {})
	if explicit_aliases is Dictionary:
		for logical in explicit_aliases.keys():
			bitmap_aliases[str(logical)] = str(explicit_aliases[logical])
	for key in bitmap_manifest.keys():
		var info: Dictionary = bitmap_manifest[key]
		var logical := int(info.get("bmp_number", 0))
		if logical > 0 and not bitmap_aliases.has(str(logical)):
			bitmap_aliases[str(logical)] = str(key)


func _bitmap_info(bitmap_id: int) -> Dictionary:
	var key := str(bitmap_id)
	if bitmap_aliases.has(key):
		var resolved: String = bitmap_aliases[key]
		if bitmap_manifest.has(resolved):
			return bitmap_manifest[resolved]
	if bitmap_manifest.has(key):
		return bitmap_manifest[key]
	if not missing_bitmap_ids.has(key):
		missing_bitmap_ids[key] = true
		print("MAP_MISSING_BITMAP id=", bitmap_id)
	return {}


func _texture_for_bitmap(bitmap_id: int) -> Texture2D:
	var key := str(bitmap_id)
	var resolved_key := key
	if bitmap_aliases.has(key):
		resolved_key = str(bitmap_aliases[key])
	if bitmap_texture_cache.has(resolved_key):
		return bitmap_texture_cache[resolved_key]
	var info: Dictionary = bitmap_manifest.get(resolved_key, {})
	if info.is_empty():
		return null
	var file_name := str(info.get("file", ""))
	if file_name.is_empty():
		return null
	var texture = load("res://assets/generated/" + file_name)
	if texture is Texture2D:
		bitmap_texture_cache[resolved_key] = texture
		return texture
	return null


func _cell_to_world(cell: Vector2i, origin: Vector2) -> Vector2:
	return origin + Vector2((cell.x + cell.y) * TILE_STEP.x, (cell.y - cell.x) * TILE_STEP.y)


func _global_to_cell(global_position: Vector2i) -> Vector2i:
	return global_position - map_crop_origin


func _activate_map_floor(floor: int) -> bool:
	var definition := _map_definition(floor)
	if definition.is_empty():
		return false
	map_sprite.texture = definition["texture"]
	map_crop_origin = definition["origin"]
	map_crop_size = definition["size"]
	map_render_origin = definition["render_origin"]
	map_sprite.name = "ServerMap%d" % floor
	return true


func _map_definition(floor: int) -> Dictionary:
	match floor:
		200:
			return {
				"texture": MAP_200_TEXTURE,
				"origin": Vector2i(280, 589),
				"size": Vector2i(80, 80),
				"render_origin": Vector2(256.0, 2152.0),
			}
		1006:
			return {
				"texture": MAP_1006_TEXTURE,
				"origin": Vector2i.ZERO,
				"size": Vector2i(30, 40),
				"render_origin": Vector2(256.0, 952.0),
			}
		_:
			return {}


func _clear_server_actors_for_map_change() -> void:
	actor_count = 0
	npc_global = Vector2i(-1, -1)
	npc_cell = Vector2i(-1, -1)
	npc_name = ""
	npc_sprite.visible = false
	_clear_all_actors()
	player_object_index = -1
	server_position_received = false
	_clear_pending_move()
	_clear_walk_target()
	pointer_held = false
	cursor_target_cell = Vector2i(-1, -1)
	if cursor_sprite != null:
		cursor_sprite.visible = false
	player_walking = false
	player_sprite.visible = false
	actor_label.text = "正在等待服务器角色/NPC数据…"


func _set_player_texture(walking: bool) -> void:
	var direction := clampi(server_direction, 0, 7)
	if walking:
		# When the direction changes mid-walk the previous cycle's frame index
		# would freeze on the wrong sprite for a beat. Restart the cycle so the
		# first frame the player sees for the new direction is the base pose.
		if walk_animation_direction != direction:
			walk_animation_direction = direction
			walk_frame_index = 0
			walk_frame_time = 0.0
	else:
		walk_animation_direction = -1
		walk_frame_index = 0
		walk_frame_time = 0.0
	var texture: Texture2D
	if walking:
		texture = _player_walk_texture(player_graphic_id, direction, walk_frame_index)
	else:
		texture = _player_idle_texture(player_graphic_id, direction)
	if texture != null:
		player_sprite.texture = texture
	if battle_player_sprite != null:
		var battle_texture := _player_idle_texture(player_graphic_id, direction)
		if battle_texture != null:
			battle_player_sprite.texture = battle_texture
	_refresh_cursor()


func _set_battle_player_texture() -> void:
	_set_player_texture(false)


func _decode_field_string(value) -> String:
	var bytes: PackedByteArray = value if value is PackedByteArray else PackedByteArray()
	var decoded := Protocol.decode_string(bytes)
	return _decode_cp936(decoded)


func _load_cp936_table() -> void:
	var file := FileAccess.open(CP936_TABLE_PATH, FileAccess.READ)
	if file != null:
		cp936_table = file.get_buffer(file.get_length())
	if cp936_table.size() != 256 * 256 * 2:
		# Keep ASCII and the protocol usable even if an old export predates the
		# generated table; non-ASCII text will be shown as replacement glyphs.
		cp936_table = PackedByteArray()


func _decode_cp936(bytes: PackedByteArray) -> String:
	var result := ""
	var index := 0
	while index < bytes.size():
		var value := bytes[index]
		if value < 0x80:
			result += String.chr(value)
			index += 1
			continue
		# CP936's single-byte Euro sign is the only non-ASCII single byte.
		if value == 0x80:
			result += "€"
			index += 1
			continue
		if index + 1 < bytes.size() and not cp936_table.is_empty():
			var offset := ((value << 8) | bytes[index + 1]) * 2
			var codepoint := cp936_table[offset] | (cp936_table[offset + 1] << 8)
			if codepoint != 0:
				result += String.chr(codepoint)
			else:
				result += "�"
			index += 2
			continue
		result += "�"
		index += 1
	return result


func _decode_field_int(value) -> int:
	var bytes: PackedByteArray = value if value is PackedByteArray else PackedByteArray()
	return Protocol.decode_int(bytes)


func _decode_base62(value: String) -> int:
	if value.is_empty():
		return -1
	var result := 0
	for character in value:
		var digit := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ".find(character)
		if digit < 0:
			return -1
		result = result * 62 + digit
	return result


func _request_player_snapshot() -> void:
	if mode != "world" or player_object_index < 0:
		return
	_send_named("C", [Protocol.encode_int(player_object_index)])


func _hex_int(value: String) -> int:
	var text := value.strip_edges()
	if text.is_empty():
		return 0
	return text.hex_to_int()


func _network_status(message: String, color: Color) -> void:
	if network_label == null:
		return
	network_label.text = message
	network_label.add_theme_color_override("font_color", color)


func _network_error(message: String) -> void:
	network_phase = "error"
	_network_status(message, DANGER)
	if mode == "login":
		login_status_label.visible = true
		login_status_label.text = message
		login_status_label.add_theme_color_override("font_color", DANGER)
	else:
		event_label.text = message
	_close_socket()


func _panel(parent: Node, rect: Rect2, color: Color, radius: int) -> Panel:
	var panel := Panel.new()
	panel.position = rect.position
	panel.size = rect.size
	var style := StyleBoxFlat.new()
	style.bg_color = color
	style.set_corner_radius_all(radius)
	style.set_border_width_all(1)
	style.border_color = Color(1, 1, 1, 0.08)
	panel.add_theme_stylebox_override("panel", style)
	parent.add_child(panel)
	return panel


func _center_top(control: Control, width: float, top: float, height: float) -> void:
	control.anchor_left = 0.5
	control.anchor_right = 0.5
	control.anchor_top = 0.0
	control.anchor_bottom = 0.0
	control.offset_left = -width * 0.5
	control.offset_right = width * 0.5
	control.offset_top = top
	control.offset_bottom = top + height


func _center_bottom(control: Control, width: float, height: float, bottom: float) -> void:
	control.anchor_left = 0.5
	control.anchor_right = 0.5
	control.anchor_top = 1.0
	control.anchor_bottom = 1.0
	control.offset_left = -width * 0.5
	control.offset_right = width * 0.5
	control.offset_top = -bottom - height
	control.offset_bottom = -bottom


func _bottom_left(control: Control, width: float, height: float, left: float, bottom: float) -> void:
	control.anchor_left = 0.0
	control.anchor_right = 0.0
	control.anchor_top = 1.0
	control.anchor_bottom = 1.0
	control.offset_left = left
	control.offset_right = left + width
	control.offset_top = -bottom - height
	control.offset_bottom = -bottom


func _bottom_right(control: Control, width: float, height: float, right: float, bottom: float) -> void:
	control.anchor_left = 1.0
	control.anchor_right = 1.0
	control.anchor_top = 1.0
	control.anchor_bottom = 1.0
	control.offset_left = -right - width
	control.offset_right = -right
	control.offset_top = -bottom - height
	control.offset_bottom = -bottom


func _add_label(parent: Node, text_value: String, rect: Rect2, font_size: int, color: Color) -> Label:
	var label := Label.new()
	label.text = text_value
	label.position = rect.position
	label.size = rect.size
	label.add_theme_font_override("font", UI_FONT)
	label.add_theme_font_size_override("font_size", font_size)
	label.add_theme_color_override("font_color", color)
	label.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
	parent.add_child(label)
	return label


func _line_edit(parent: Node, placeholder: String, rect: Rect2, secret: bool) -> LineEdit:
	var edit := LineEdit.new()
	edit.placeholder_text = placeholder
	edit.position = rect.position
	edit.size = rect.size
	edit.add_theme_font_override("font", UI_FONT)
	edit.add_theme_font_size_override("font_size", 17)
	edit.add_theme_color_override("font_color", TEXT)
	edit.add_theme_color_override("font_placeholder_color", MUTED)
	edit.add_theme_color_override("caret_color", ACCENT)
	edit.add_theme_stylebox_override("normal", _input_style(Color("#0b1718")))
	edit.add_theme_stylebox_override("focus", _input_style(Color("#152b2c"), ACCENT))
	edit.secret = secret
	parent.add_child(edit)
	return edit


func _input_style(fill: Color, border: Color = Color(1, 1, 1, 0.08)) -> StyleBoxFlat:
	var style := StyleBoxFlat.new()
	style.bg_color = fill
	style.border_color = border
	style.set_border_width_all(1)
	style.set_corner_radius_all(12)
	style.content_margin_left = 14
	style.content_margin_right = 14
	return style


func _button(parent: Node, text_value: String, rect: Rect2, action: Callable, color: Color, font_size: int) -> Button:
	var button := Button.new()
	button.text = text_value
	button.position = rect.position
	button.size = rect.size
	button.focus_mode = Control.FOCUS_NONE
	button.add_theme_font_override("font", UI_FONT)
	button.add_theme_font_size_override("font_size", font_size)
	button.add_theme_color_override("font_color", TEXT)
	button.add_theme_color_override("font_hover_color", TEXT)
	button.add_theme_color_override("font_pressed_color", ACCENT)
	for state in ["normal", "hover", "pressed", "focus", "disabled"]:
		var style := StyleBoxFlat.new()
		style.bg_color = color.lightened(0.08 if state == "hover" else 0.0)
		style.set_corner_radius_all(14)
		button.add_theme_stylebox_override(state, style)
	button.pressed.connect(action)
	parent.add_child(button)
	return button
