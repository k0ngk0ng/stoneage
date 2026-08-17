extends SceneTree
## Headless smoke test for the online portrait client.
##
## Network gameplay is exercised against the running gateway on a device; this
## test covers the deterministic client-side invariants that must not regress:
## login starts before character selection, the PC-sized map rectangle is
## derived from the authoritative coordinate, and transport notices never
## become chat spam.

const MAIN_SCENE := "res://main.tscn"
const Protocol = preload("res://scripts/stoneage_named_protocol.gd")


func _init() -> void:
	call_deferred("_run")


func _run() -> void:
	var scene: Node = load(MAIN_SCENE).instantiate()
	root.add_child(scene)
	await process_frame
	_assert(scene.mode == "login", "online client must start at the username/password login")
	_assert(scene.character == "", "character name must not be requested on the login page")
	_assert(not scene.login_status_label.visible, "idle login page must not show a redundant footer notice")

	scene.mode = "world"
	scene.server_floor = 200
	scene.server_position_received = true
	scene.server_position = Vector2i(322, 626)
	var rect: Rect2i = scene._pc_map_rect()
	_assert(rect == Rect2i(302, 610, 37, 37), "PC MAP.H rectangle must be 37x37 around the server coordinate")

	scene._append_chat_line("系统", "服务器已连接", false)
	scene._append_chat_line("系统", "真正的任务消息", false)
	_assert(scene.chat_lines.size() == 1, "connection-only notices must stay out of chat")
	_assert(scene.chat_lines[0].contains("真正的任务消息"), "meaningful server text must remain in chat")

	# WN menu payloads are internal PC window data, while message windows are
	# player-facing notices and belong in the same transcript.
	scene._handle_window_notice([
		Protocol.encode_int(2).to_utf8_buffer(), Protocol.encode_int(0).to_utf8_buffer(), Protocol.encode_int(0).to_utf8_buffer(),
		Protocol.encode_int(0).to_utf8_buffer(), Protocol.encode_string("菜单选项|内部数据").to_utf8_buffer(),
	])
	_assert(scene.chat_lines.size() == 1, "WN menu payloads must not become chat lines")
	scene._handle_window_notice([
		Protocol.encode_int(0).to_utf8_buffer(), Protocol.encode_int(1).to_utf8_buffer(), Protocol.encode_int(0).to_utf8_buffer(),
		Protocol.encode_int(0).to_utf8_buffer(), Protocol.encode_string("任务通知").to_utf8_buffer(),
	])
	_assert(scene.chat_lines.size() == 2, "WN message windows must remain visible in chat")

	# Compact mode keeps incoming count; opening the bar clears it and exposes
	# the input/send controls, then closing it returns to the compact bar.
	scene._set_chat_expanded(false)
	scene.chat_unread_count = 0
	scene._append_chat_line("系统", "折叠后消息", false)
	_assert(scene.chat_unread_count == 1, "collapsed chat must count unread messages")
	scene._set_chat_expanded(true)
	_assert(scene.chat_unread_count == 0 and scene.chat_input.visible, "expanded chat must clear unread and show input")
	scene._set_chat_expanded(false)
	_assert(not scene.chat_input.visible and scene.chat_toggle_button.text == "展开", "collapsed chat must hide input")

	print("online mobile smoke passed")
	quit()


func _assert(condition: bool, message: String) -> void:
	if not condition:
		push_error("Online mobile smoke failed: " + message)
		quit(1)
