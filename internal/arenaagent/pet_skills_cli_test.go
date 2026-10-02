package arenaagent

import (
	"flag"
	"io"
	"testing"
)

func TestPetSkillFlagsRejectInvalidNativeLoadouts(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
	}{
		{"1,2,20", 131}, {"20, 2,1", 131}, {"1,2,3,60,80,90,110", 127},
		{"", 0}, {"1,1", 0}, {"0", 0}, {"999", 0}, {"1,2,3,60,80,90,110,20", 0}, {"1,2,", 0},
	} {
		f := flag.NewFlagSet("test", flag.ContinueOnError)
		f.SetOutput(io.Discard)
		mask := 0
		petSkillFlags(f, &mask)
		err := f.Parse([]string{"--pet-skills", tc.input})
		if (err == nil) != (tc.want != 0) || mask != tc.want {
			t.Fatal(tc.input, mask, err)
		}
	}
}
