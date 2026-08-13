package namedproto

import "fmt"

const (
	ringoCharacterCount = 256
	ringoNodeCount      = 512
	ringoCodeBits       = 9
)

type ringoNode struct {
	character byte
	parent    int
	brother   int
	child     int
}

type bitWriter struct {
	data     []byte
	bitCount int
}

func (writer *bitWriter) write(width int, value uint32) {
	neededBits := writer.bitCount + width
	neededBytes := (neededBits + 7) / 8
	for len(writer.data) < neededBytes {
		writer.data = append(writer.data, 0)
	}
	for bit := 0; bit < width; bit++ {
		if value&(1<<bit) != 0 {
			position := writer.bitCount + bit
			writer.data[position/8] |= 1 << (position % 8)
		}
	}
	writer.bitCount = neededBits
}

type bitReader struct {
	data     []byte
	bitCount int
}

func (reader *bitReader) read(width int) (uint32, error) {
	if reader.bitCount+width > len(reader.data)*8 {
		return 0, fmt.Errorf("%w: truncated Ringo bit stream", ErrMalformed)
	}
	var result uint32
	for bit := 0; bit < width; bit++ {
		position := reader.bitCount + bit
		if reader.data[position/8]&(1<<(position%8)) != 0 {
			result |= 1 << bit
		}
	}
	reader.bitCount += width
	return result, nil
}

func initializeRingoNodes() [ringoNodeCount]ringoNode {
	var nodes [ringoNodeCount]ringoNode
	for index := 0; index <= ringoCharacterCount; index++ {
		nodes[index].character = byte(index)
		nodes[index].brother = index + 1
	}
	nodes[ringoCharacterCount].brother = 0
	return nodes
}

func ringoCompress(input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("%w: cannot Ringo-compress an empty payload", ErrMalformed)
	}
	nodes := initializeRingoNodes()
	freeNode := ringoCharacterCount + 1
	word := int(input[0])
	textIndex := 1
	var writer bitWriter

	for {
		character := ringoCharacterCount
		if textIndex < len(input) {
			character = int(input[textIndex])
		}

		match := nodes[word].child
		for match > 0 && int(nodes[match].character) != character {
			match = nodes[match].brother
		}
		if match > 0 {
			word = match
		} else {
			writer.write(ringoCodeBits, uint32(word))
			if freeNode < ringoNodeCount {
				nodes[freeNode].parent = word
				nodes[freeNode].character = byte(character)
				nodes[freeNode].brother = nodes[word].child
				nodes[word].child = freeNode
				freeNode++
			}
			word = character
		}

		if textIndex == len(input)+1 {
			break
		}
		textIndex++
	}
	return writer.data, nil
}

func ringoDecompress(input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("%w: empty Ringo stream", ErrMalformed)
	}
	nodes := initializeRingoNodes()
	freeNode := ringoCharacterCount + 1
	reader := bitReader{data: input}
	result := make([]byte, 0, len(input)*2)
	stack := make([]int, 0, ringoNodeCount)
	character := 0
	word := 0

	for {
		encoded, err := reader.read(ringoCodeBits)
		if err != nil {
			return nil, err
		}
		value := int(encoded)
		if value == ringoCharacterCount {
			break
		}
		if value >= ringoNodeCount {
			return nil, fmt.Errorf("%w: Ringo node %d is outside the dictionary", ErrMalformed, value)
		}

		stack = stack[:0]
		if value >= freeNode {
			stack = append(stack, character)
			character = word
		} else {
			character = value
		}
		for character > ringoCharacterCount {
			if character >= ringoNodeCount || len(stack) >= ringoNodeCount {
				return nil, fmt.Errorf("%w: corrupt Ringo dictionary chain", ErrMalformed)
			}
			stack = append(stack, int(nodes[character].character))
			character = nodes[character].parent
		}
		stack = append(stack, character)
		for index := len(stack) - 1; index >= 0; index-- {
			result = append(result, byte(stack[index]))
		}

		if len(result) > 1 && freeNode < ringoNodeCount {
			if word < 0 || word >= ringoNodeCount {
				return nil, fmt.Errorf("%w: corrupt Ringo parent", ErrMalformed)
			}
			nodes[freeNode].parent = word
			nodes[freeNode].character = byte(character)
			nodes[freeNode].brother = nodes[word].child
			nodes[word].child = freeNode
			freeNode++
		}
		word = value
		if len(result) > 4*1024*1024 {
			return nil, fmt.Errorf("%w: Ringo output exceeds 4 MiB", ErrMalformed)
		}
	}
	return result, nil
}
