package ainavigation

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ParseMapset reads the server's space-delimited mapset.txt image table.
// The C loader starts its data fields at the fourth one-based token, which is
// fields[3] after splitting in Go. Missing optional fields retain the server
// defaults (walkable=1, have-height=0).
func ParseMapset(r io.Reader) (ImageTable, error) {
	if r == nil {
		return ImageTable{}, fmt.Errorf("%w: nil mapset reader", ErrInvalidMap)
	}
	table := ImageTable{Images: make(map[uint16]ImageRule)}
	scanner := bufio.NewScanner(r)
	// The deployed file has short rows, but keep the parser safe for a future
	// generated table with a long comment or extension field.
	scanner.Buffer(make([]byte, 4096), 1<<20)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		image, err := parseMapsetUint16(fields[0])
		if err != nil {
			return ImageTable{}, fmt.Errorf("%w: mapset line %d image: %v", ErrInvalidMap, lineNumber, err)
		}
		rule := ImageRule{Walkable: 1}
		if len(fields) > 3 {
			rule.Walkable, err = strconv.Atoi(fields[3])
			if err != nil {
				return ImageTable{}, fmt.Errorf("%w: mapset line %d walkable: %v", ErrInvalidMap, lineNumber, err)
			}
		}
		if len(fields) > 4 {
			var height int
			height, err = strconv.Atoi(fields[4])
			if err != nil {
				return ImageTable{}, fmt.Errorf("%w: mapset line %d height: %v", ErrInvalidMap, lineNumber, err)
			}
			rule.HaveHeight = height == 1
		}
		// MAP_imgfilt is assigned for every row and therefore the last
		// duplicate row wins. Preserve that behavior instead of rejecting a
		// table which the legacy server itself would accept.
		table.Images[image] = rule
	}
	if err := scanner.Err(); err != nil {
		return ImageTable{}, fmt.Errorf("%w: read mapset: %v", ErrInvalidMap, err)
	}
	if len(table.Images) == 0 {
		return ImageTable{}, fmt.Errorf("%w: mapset has no image rows", ErrInvalidMap)
	}
	return table, nil
}

func parseMapsetUint16(value string) (uint16, error) {
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil || n < 0 || n > 65534 {
		if err == nil {
			err = errors.New("image number must be between 0 and 65534")
		}
		return 0, err
	}
	return uint16(n), nil
}
