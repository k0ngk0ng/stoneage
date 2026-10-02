package arenaagent

import (
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
)

func petSkillFlags(f *flag.FlagSet, mask *int) {
	f.Func("pet-skills", "active pet skill IDs, at most seven of 1,2,3,60,80,90,110,20 (20: guardian); default preserves original seven", func(raw string) error {
		value := 0
		for _, token := range strings.Split(raw, ",") {
			id, err := strconv.Atoi(strings.TrimSpace(token))
			if err != nil {
				return fmt.Errorf("pet skills must be comma-separated supported integer IDs")
			}
			bit := 0
			for i, supported := range []int{1, 2, 3, 60, 80, 90, 110, 20} {
				if id == supported {
					bit = 1 << i
				}
			}
			if bit == 0 || value&bit != 0 {
				return fmt.Errorf("unsupported or duplicate pet skill %d", id)
			}
			value |= bit
		}
		if !battleenv.ValidPetSkillMask(value) {
			return fmt.Errorf("a native pet has at most seven usable skill slots")
		}
		*mask = value
		return nil
	})
}
