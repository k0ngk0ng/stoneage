extends SceneTree

const Protocol = preload("res://scripts/stoneage_named_protocol.gd")

func _init() -> void:
	var captured := "HBZovLTemtm8s7fgadloSK8dIILiINuPLPGzJ-8\n".to_utf8_buffer()
	var raw := Protocol.decode_packet(captured)
	assert(raw.get_string_from_ascii() == "1 ClientLogin probe local ")
	var encoded := Protocol.encode_packet(raw)
	assert(encoded == captured)
	var message := Protocol.parse_message(raw)
	assert(message["function"] == "ClientLogin")
	assert(message["fields"].size() == 2)
	assert(Protocol.decode_string(message["fields"][0]).get_string_from_ascii() == "probe")
	var long_raw := ("42 MSG 0 " + "StoneAge named protocol map payload | ".repeat(64)).to_utf8_buffer()
	var long_packet := Protocol.encode_packet(long_raw)
	assert(Protocol.decode_packet(long_packet) == long_raw)
	print("named protocol smoke passed")
	quit()

