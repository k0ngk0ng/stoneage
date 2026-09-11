package gamecatalog

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

type previewSprite struct {
	Actions []struct {
		Direction int `json:"direction"`
		Action    int `json:"action"`
		Frames    []struct {
			File    string `json:"file"`
			XOffset int    `json:"xoffset"`
			YOffset int    `json:"yoffset"`
		} `json:"frames"`
	} `json:"actions"`
}

// ParsePetPreviews uses the same native sprite selection as petDetailSprite in
// client/web: direction 1, action 3 (STAND). The numeric sprite ID is NOT a
// physical bitmap ID. actor_bitmaps historically contains such collisions.
// Decode one sprite at a time so the full animation atlas is not retained.
func ParsePetPreviews(reader io.Reader) (map[int]Asset, error) {
	decoder := json.NewDecoder(reader)
	result := map[int]Asset{}
	if err := readSpriteObject(decoder, result); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing sprite manifest data")
	}
	return result, nil
}

func readSpriteObject(decoder *json.Decoder, result map[int]Asset) error {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("invalid sprite manifest object")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("invalid sprite key")
		}
		if key == "sprites" {
			if err := readSpriteObject(decoder, result); err != nil {
				return err
			}
			continue
		}
		id, err := strconv.Atoi(key)
		if err != nil {
			var discard json.RawMessage
			if err = decoder.Decode(&discard); err != nil {
				return err
			}
			continue
		}
		var sprite previewSprite
		if err = decoder.Decode(&sprite); err != nil {
			return err
		}
		best := -1
		for i, action := range sprite.Actions {
			if action.Action != 3 || len(action.Frames) == 0 || action.Frames[0].File == "" {
				continue
			}
			if best == -1 {
				best = i
			}
			if action.Direction == 1 {
				best = i
				break
			}
		}
		if best >= 0 {
			frame := sprite.Actions[best].Frames[0]
			result[id] = Asset{LogicalID: id, File: frame.File, XOffset: frame.XOffset, YOffset: frame.YOffset}
		}
	}
	_, err = decoder.Token()
	return err
}
