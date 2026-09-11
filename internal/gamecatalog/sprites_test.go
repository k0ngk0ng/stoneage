package gamecatalog

import (
	"strings"
	"testing"
)

func TestPetPreviewSelectsNativeStandInsteadOfBitmapNumber(t *testing.T) {
	const sprites = `{"100250":{"actions":[{"direction":0,"action":0,"frames":[{"file":"attack.png"}]},{"direction":0,"action":3,"frames":[{"file":"back.png"}]},{"direction":1,"action":3,"frames":[{"file":"bitmaps/bitmap_95929.png","xoffset":-27,"yoffset":-34}]}]},"100251":{"actions":[{"direction":0,"action":3,"frames":[{"file":"fallback.png"}]}]},"100252":{"actions":[{"direction":1,"action":0,"frames":[{"file":"attack-only.png"}]}]}}`
	for _, data := range []string{sprites, `{"actor_bitmaps":{"100250":"wrong.png"},"sprites":` + sprites + `}`} {
		got, err := ParsePetPreviews(strings.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if got[100250].File != "bitmaps/bitmap_95929.png" || got[100250].XOffset != -27 {
			t.Fatalf("wrong native frame: %+v", got[100250])
		}
		if got[100251].File != "fallback.png" {
			t.Fatalf("missing stand fallback: %+v", got)
		}
		if _, ok := got[100252]; ok {
			t.Fatal("attack frame used as stand preview")
		}
	}
}
