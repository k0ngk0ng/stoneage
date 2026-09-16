package ainavigation

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

const (
	ls2MapMagic       = "LS2MAP"
	ls2MapHeaderBytes = 44 // magic + id + 32-byte label + width + height
	maxMapDimension   = 4096
	maxMapCells       = 16 * 1024 * 1024
)

// ParseLS2MAP parses one server map file. LS2MAP stores all integer fields in
// network byte order, including the tile and object arrays. The old C loader
// intentionally ignores bytes after the two arrays; trailing bytes are
// therefore accepted to preserve compatibility with the two historical files
// shipped in the repository.
func ParseLS2MAP(r io.Reader, images ImageTable) (FloorMap, error) {
	return parseLS2MAPWithLimits(r, images, maxMapDimension, maxMapCells)
}

func parseLS2MAPWithLimits(r io.Reader, images ImageTable, dimensionLimit, cellLimit int) (FloorMap, error) {
	if r == nil {
		return FloorMap{}, fmt.Errorf("%w: nil LS2MAP reader", ErrInvalidMap)
	}
	header := make([]byte, ls2MapHeaderBytes)
	if _, err := io.ReadFull(r, header); err != nil {
		return FloorMap{}, fmt.Errorf("%w: truncated header: %v", ErrInvalidMap, err)
	}
	if string(header[:6]) != ls2MapMagic {
		return FloorMap{}, fmt.Errorf("%w: magic %q", ErrInvalidMap, string(header[:6]))
	}
	id := int(binary.BigEndian.Uint16(header[6:8]))
	width := int(binary.BigEndian.Uint16(header[40:42]))
	height := int(binary.BigEndian.Uint16(header[42:44]))
	if dimensionLimit <= 0 || dimensionLimit > maxMapDimension {
		return FloorMap{}, fmt.Errorf("%w: invalid dimension limit %d", ErrInvalidMap, dimensionLimit)
	}
	if cellLimit <= 0 || cellLimit > maxMapCells {
		return FloorMap{}, fmt.Errorf("%w: invalid cell limit %d", ErrInvalidMap, cellLimit)
	}
	if width <= 0 || height <= 0 || width > dimensionLimit || height > dimensionLimit {
		return FloorMap{}, fmt.Errorf("%w: floor %d dimensions %dx%d", ErrInvalidMap, id, width, height)
	}
	if width > cellLimit/height {
		return FloorMap{}, fmt.Errorf("%w: floor %d has too many cells", ErrInvalidMap, id)
	}
	cells := width * height
	tiles := make([]uint16, cells)
	objects := make([]uint16, cells)
	if err := readUint16s(r, tiles); err != nil {
		return FloorMap{}, fmt.Errorf("%w: floor %d tile data: %v", ErrInvalidMap, id, err)
	}
	if err := readUint16s(r, objects); err != nil {
		return FloorMap{}, fmt.Errorf("%w: floor %d object data: %v", ErrInvalidMap, id, err)
	}
	for index, image := range tiles {
		if _, ok := images.Images[image]; !ok {
			return FloorMap{}, fmt.Errorf("%w: floor %d tile cell %d references unknown image %d", ErrInvalidMap, id, index, image)
		}
	}
	for index, image := range objects {
		if _, ok := images.Images[image]; !ok {
			return FloorMap{}, fmt.Errorf("%w: floor %d object cell %d references unknown image %d", ErrInvalidMap, id, index, image)
		}
	}
	label := header[8:40]
	if end := strings.IndexByte(string(label), 0); end >= 0 {
		label = label[:end]
	}
	return FloorMap{ID: id, Width: width, Height: height, ShowString: string(label), Tiles: tiles, Objects: objects}, nil
}

// ParseMap is a short alias for ParseLS2MAP for callers which already know
// the supplied reader contains a server map.
func ParseMap(r io.Reader, images ImageTable) (FloorMap, error) {
	return ParseLS2MAP(r, images)
}

func readUint16s(r io.Reader, values []uint16) error {
	if len(values) == 0 {
		return nil
	}
	bytes := make([]byte, len(values)*2)
	if _, err := io.ReadFull(r, bytes); err != nil {
		return err
	}
	for index := range values {
		values[index] = binary.BigEndian.Uint16(bytes[index*2:])
	}
	return nil
}
