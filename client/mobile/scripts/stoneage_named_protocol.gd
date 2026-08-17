class_name StoneAgeNamedProtocol
extends RefCounted
## Byte-oriented implementation of the function-name LSSPROTO framing used by
## the preserved PC client and the local compatibility gateway.
##
## The mobile client must speak this protocol directly.  It is intentionally
## kept independent from the old 1.82 source: only the wire format is ported.

const JENCODE_KEY := 51526931
const BASE64_CHARS := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+-"
const BASE62_CHARS := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
const RINGO_CHARACTER_COUNT := 256
const RINGO_NODE_COUNT := 512
const RINGO_CODE_BITS := 9


static func encode_string(value: String) -> String:
	return encode_bytes(value.to_utf8_buffer())


static func encode_bytes(value: PackedByteArray) -> String:
	var result := ""
	for character in value:
		match character:
			92: # backslash
				result += "\\\\"
			32:
				result += "\\S"
			10:
				result += "\\n"
			13:
				result += "\\r"
			_:
				result += String.chr(character)
	return result


static func decode_string(value: PackedByteArray) -> PackedByteArray:
	var result := PackedByteArray()
	var index := 0
	while index < value.size():
		if value[index] != 92:
			result.append(value[index])
			index += 1
			continue
		if index + 1 >= value.size():
			break
		index += 1
		match value[index]:
			83: # S
				result.append(32)
			110: # n
				result.append(10)
			114: # r
				result.append(13)
			92:
				result.append(92)
			_:
				result.append(value[index])
		index += 1
	return result


static func encode_int(value: int) -> String:
	if value == 0:
		return "0"
	var number := value
	var negative := number < 0
	if negative:
		number = -number
	var reversed := ""
	while number > 0:
		var digit := number % 62
		reversed = BASE62_CHARS.substr(digit, 1) + reversed
		number = number / 62
	if negative:
		return "-" + reversed
	return reversed


static func decode_int(value: PackedByteArray) -> int:
	if value.is_empty():
		return 0
	var text := value.get_string_from_ascii()
	var negative := text.begins_with("-")
	if negative:
		text = text.substr(1)
	var result := 0
	for character in text:
		var digit := BASE62_CHARS.find(character)
		if digit < 0:
			return 0
		result = result * 62 + digit
	return -result if negative else result


static func raw_message(message_id: int, function_name: String, fields: Array[String]) -> PackedByteArray:
	var text := "%d %s " % [message_id, function_name]
	for field in fields:
		text += field + " "
	return text.to_utf8_buffer()


static func encode_packet(raw: PackedByteArray) -> PackedByteArray:
	if raw.is_empty():
		return PackedByteArray()
	var string_length := raw.size() + 1
	var compressed := PackedByteArray()
	if string_length < 100:
		var flag := string_length
		if flag % 2 == 1:
			flag += 1
		compressed.append(flag)
		compressed.append_array(raw)
		compressed.append(0)
	else:
		var flag := string_length
		if flag % 2 == 0:
			flag += 1
		compressed.append(flag)
		compressed.append_array(_ringo_compress(raw))
	return _encode64(_jencode(compressed)) + PackedByteArray([10])


static func decode_packet(packet: PackedByteArray) -> PackedByteArray:
	var input := packet
	if not input.is_empty() and input[input.size() - 1] == 10:
		input = input.slice(0, input.size() - 1)
	var jencoded := _decode64(input)
	var compressed := _jdecode(jencoded)
	if compressed.size() < 2:
		return PackedByteArray()
	var raw := PackedByteArray()
	if compressed[0] % 2 == 0:
		raw = compressed.slice(1)
		var terminator := raw.find(0)
		if terminator >= 0:
			raw = raw.slice(0, terminator)
	else:
		raw = _ringo_decompress(compressed.slice(1))
	return raw


static func parse_message(raw: PackedByteArray) -> Dictionary:
	var tokens: Array[PackedByteArray] = []
	var start := 0
	for index in range(raw.size()):
		if raw[index] == 32:
			tokens.append(raw.slice(start, index))
			start = index + 1
	if start <= raw.size():
		tokens.append(raw.slice(start))
	if tokens.size() < 3:
		return {}
	var fields: Array[PackedByteArray] = []
	# The final token is empty because RawMessage appends one trailing space.
	for index in range(2, tokens.size() - 1):
		fields.append(tokens[index])
	return {
		"id": int(tokens[0].get_string_from_ascii()),
		"function": tokens[1].get_string_from_ascii(),
		"fields": fields,
	}


static func _encode64(input: PackedByteArray) -> PackedByteArray:
	var result := PackedByteArray()
	var index := 0
	while index < input.size():
		var remaining := input.size() - index
		var first := input[index]
		var second := input[index + 1] if remaining > 1 else 0
		var third := input[index + 2] if remaining > 2 else 0
		result.append(BASE64_CHARS.unicode_at((first & 0xfc) >> 2))
		result.append(BASE64_CHARS.unicode_at(((first & 0x03) << 4) | ((second & 0xf0) >> 4)))
		if remaining > 1:
			result.append(BASE64_CHARS.unicode_at(((second & 0x0f) << 2) | ((third & 0xc0) >> 6)))
		if remaining > 2:
			result.append(BASE64_CHARS.unicode_at(third & 0x3f))
		index += 3
	return result


static func _decode64(input: PackedByteArray) -> PackedByteArray:
	var result := PackedByteArray()
	var index := 0
	while index < input.size():
		var remaining := input.size() - index
		var values := [0, 0, 0, 0]
		var limit := mini(remaining, 4)
		for offset in range(limit):
			values[offset] = BASE64_CHARS.find(String.chr(input[index + offset]))
		if limit < 2 or values[0] < 0 or values[1] < 0:
			return PackedByteArray()
		result.append((values[0] << 2) | ((values[1] & 0x30) >> 4))
		if limit >= 3:
			if values[2] < 0:
				return PackedByteArray()
			result.append(((values[1] & 0x0f) << 4) | ((values[2] & 0x3c) >> 2))
		if limit >= 4:
			if values[3] < 0:
				return PackedByteArray()
			result.append(((values[2] & 0x03) << 6) | (values[3] & 0x3f))
		index += 4
	return result


static func _jencode(input: PackedByteArray) -> PackedByteArray:
	var source := PackedByteArray(input)
	var sum := 0
	for index in range(source.size()):
		sum = (sum + source[index]) & 0xff
		if JENCODE_KEY % 7 == index % 5 or JENCODE_KEY % 2 == index % 2:
			source[index] = 255 - source[index]
	var position := JENCODE_KEY % source.size()
	var result := PackedByteArray()
	for index in range(source.size() + 1):
		if position > index:
			result.append((source[index] + sum * ((index * index) % 3)) & 0xff)
		elif position == index:
			result.append(sum)
		else:
			result.append((source[index - 1] + sum * ((index * index) % 7)) & 0xff)
	return result


static func _jdecode(input: PackedByteArray) -> PackedByteArray:
	if input.size() < 2:
		return PackedByteArray()
	var decoded_length := input.size() - 1
	var position := JENCODE_KEY % decoded_length
	var sum := input[position]
	var result := PackedByteArray()
	result.resize(decoded_length)
	for index in range(input.size()):
		if position > index:
			result[index] = (input[index] - sum * ((index * index) % 3)) & 0xff
		elif position < index:
			result[index - 1] = (input[index] - sum * ((index * index) % 7)) & 0xff
	for index in range(result.size()):
		if JENCODE_KEY % 7 == index % 5 or JENCODE_KEY % 2 == index % 2:
			result[index] = 255 - result[index]
	return result


static func _ringo_compress(input: PackedByteArray) -> PackedByteArray:
	if input.is_empty():
		return PackedByteArray()
	var characters := PackedInt32Array()
	var parents := PackedInt32Array()
	var brothers := PackedInt32Array()
	var children := PackedInt32Array()
	characters.resize(RINGO_NODE_COUNT)
	parents.resize(RINGO_NODE_COUNT)
	brothers.resize(RINGO_NODE_COUNT)
	children.resize(RINGO_NODE_COUNT)
	for index in range(RINGO_CHARACTER_COUNT + 1):
		characters[index] = index
		brothers[index] = index + 1
	brothers[RINGO_CHARACTER_COUNT] = 0
	var free_node := RINGO_CHARACTER_COUNT + 1
	var word := input[0]
	var text_index := 1
	var writer := PackedByteArray()
	var bit_count := 0
	while true:
		var character := RINGO_CHARACTER_COUNT
		if text_index < input.size():
			character = input[text_index]
		var match_node := children[word]
		while match_node > 0 and characters[match_node] != character:
			match_node = brothers[match_node]
		if match_node > 0:
			word = match_node
		else:
			_write_bits(writer, bit_count, RINGO_CODE_BITS, word)
			bit_count += RINGO_CODE_BITS
			if free_node < RINGO_NODE_COUNT:
				parents[free_node] = word
				characters[free_node] = character
				brothers[free_node] = children[word]
				children[word] = free_node
				free_node += 1
			word = character
		if text_index == input.size() + 1:
			break
		text_index += 1
	return writer


static func _write_bits(data: PackedByteArray, bit_count: int, width: int, value: int) -> void:
	var needed_bits := bit_count + width
	var needed_bytes := (needed_bits + 7) / 8
	while data.size() < needed_bytes:
		data.append(0)
	for bit in range(width):
		if (value & (1 << bit)) != 0:
			var position := bit_count + bit
			data[position / 8] |= 1 << (position % 8)


static func _ringo_decompress(input: PackedByteArray) -> PackedByteArray:
	if input.is_empty():
		return PackedByteArray()
	var characters := PackedInt32Array()
	var parents := PackedInt32Array()
	var brothers := PackedInt32Array()
	var children := PackedInt32Array()
	characters.resize(RINGO_NODE_COUNT)
	parents.resize(RINGO_NODE_COUNT)
	brothers.resize(RINGO_NODE_COUNT)
	children.resize(RINGO_NODE_COUNT)
	for index in range(RINGO_CHARACTER_COUNT + 1):
		characters[index] = index
		brothers[index] = index + 1
	brothers[RINGO_CHARACTER_COUNT] = 0
	var free_node := RINGO_CHARACTER_COUNT + 1
	var bit_count := 0
	var result := PackedByteArray()
	var word := 0
	var character := 0
	while bit_count + RINGO_CODE_BITS <= input.size() * 8:
		var encoded := 0
		for bit in range(RINGO_CODE_BITS):
			var position := bit_count + bit
			if (input[position / 8] & (1 << (position % 8))) != 0:
				encoded |= 1 << bit
		bit_count += RINGO_CODE_BITS
		var value := encoded
		if value == RINGO_CHARACTER_COUNT:
			break
		if value >= RINGO_NODE_COUNT:
			return PackedByteArray()
		var stack: Array[int] = []
		if value >= free_node:
			stack.append(character)
			character = word
		else:
			character = value
		while character > RINGO_CHARACTER_COUNT:
			if character >= RINGO_NODE_COUNT or stack.size() >= RINGO_NODE_COUNT:
				return PackedByteArray()
			stack.append(characters[character])
			character = parents[character]
		stack.append(character)
		for index in range(stack.size() - 1, -1, -1):
			result.append(stack[index])
		if result.size() > 1 and free_node < RINGO_NODE_COUNT:
			parents[free_node] = word
			characters[free_node] = character
			brothers[free_node] = children[word]
			children[word] = free_node
			free_node += 1
		word = value
		if result.size() > 4 * 1024 * 1024:
			return PackedByteArray()
	return result

