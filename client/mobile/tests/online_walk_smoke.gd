extends SceneTree
## Live integration check for the portrait client's walk path.
##
## Unlike mvp_smoke.gd this test requires the local gateway and the player_demo test
## account. It deliberately sends real W packets and verifies that the phone
## unlocks from the pending state using the server's C snapshot, not a local
## coordinate simulation.

const MAIN_SCENE := "res://main.tscn"
const ACCOUNT := "player_demo"
const PASSWORD := "123123"

var scene: Node


func _init() -> void:
	call_deferred("_run")


func _run() -> void:
	scene = load(MAIN_SCENE).instantiate()
	root.add_child(scene)
	await process_frame
	scene.account_edit.text = ACCOUNT
	scene.password_edit.text = PASSWORD
	scene._on_login_pressed()
	await _wait_for(func() -> bool: return scene.network_phase == "character_select", 8.0, "character list")
	if scene.character_names.is_empty():
		_fail("account has no character")
		return
	scene.character_option.select(0)
	scene._on_character_selected()
	await _wait_for(func() -> bool: return scene.mode == "world", 8.0, "character entry")
	await _wait_for(func() -> bool:
		return scene.map_loaded and scene.server_position_received and scene.player_object_index >= 0
	, 12.0, "initial map/character snapshot")

	var accepted_steps := 0
	var attempted := 0
	var directions := [Vector2i(1, 0), Vector2i(0, 1), Vector2i(-1, 0), Vector2i(0, -1)]
	for delta in directions:
		if accepted_steps >= 2:
			break
		var before: Vector2i = scene.server_position
		scene._on_move(delta)
		attempted += 1
		await _wait_for(func() -> bool: return not scene.pending_move, 3.5, "move confirmation")
		if scene.server_position != before:
			accepted_steps += 1

	# Try the first available direction again. This proves a rejected step (if
	# any) cannot leave the input pad locked forever, while two accepted steps
	# prove that W/C can be chained without local fake movement.
	if accepted_steps < 2:
		for delta in directions:
			if accepted_steps >= 2:
				break
			var before: Vector2i = scene.server_position
			scene._on_move(delta)
			attempted += 1
			await _wait_for(func() -> bool: return not scene.pending_move, 3.5, "second move confirmation")
			if scene.server_position != before:
				accepted_steps += 1

	if accepted_steps < 2:
		_fail("only %d accepted server steps after %d attempts (position=%s)" % [accepted_steps, attempted, scene.server_position])
		return
	print("online walk smoke passed: accepted_steps=%d final=%s" % [accepted_steps, scene.server_position])
	quit()


func _wait_for(predicate: Callable, seconds: float, label: String) -> void:
	var deadline := Time.get_ticks_msec() + int(seconds * 1000.0)
	while Time.get_ticks_msec() < deadline:
		if predicate.call():
			return
		await create_timer(0.05).timeout
	_fail("timed out waiting for " + label)


func _fail(message: String) -> void:
	push_error("Online walk smoke failed: " + message)
	quit(1)
