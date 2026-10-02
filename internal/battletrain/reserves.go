package battletrain

import (
	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"math/rand"
)

func reserveCount(rows [][]battleenv.ReservePet) int {
	if len(rows) == 0 {
		return 0
	}
	return len(rows[0])
}

func randomReserves(members, count, points int, rng *rand.Rand, active ...int) [][]battleenv.ReservePet {
	if count == 0 {
		return nil
	}
	rows := make([][]battleenv.ReservePet, members)
	for i := range rows {
		for j := 0; j < count; j++ {
			// Always offer ordinary attack/guard plus one audited specialist skill.
			rows[i] = append(rows[i], battleenv.ReservePet{Build: allocation(points, rng), SkillMask: randomReserveSkills(rng, active...)})
		}
	}
	return rows
}

func reserveSpecialists(active int) []int {
	if active == 0 {
		active = 127
	}
	var result []int
	for bit := 2; bit < 8; bit++ {
		if active&(1<<bit) != 0 {
			result = append(result, 1<<bit)
		}
	}
	return result
}

func randomReserveSkills(rng *rand.Rand, active ...int) int {
	mask := 0
	if len(active) > 0 {
		mask = active[0]
	}
	choices := reserveSpecialists(mask)
	if len(choices) == 0 {
		return 3
	}
	return 3 | choices[rng.Intn(len(choices))]
}

func applyPetSkills(s *battleenv.Scenario, mask int) {
	if mask == 0 {
		return
	}
	s.PetSkillMasks = make([]int, len(s.PetBuilds))
	for i := range s.PetSkillMasks {
		s.PetSkillMasks[i] = mask
	}
}

func scenarioPetSkillsMatch(s battleenv.Scenario, mask int) bool {
	if mask == 0 {
		return len(s.PetSkillMasks) == 0
	}
	if len(s.PetSkillMasks) != 2*s.Mode {
		return false
	}
	for _, actual := range s.PetSkillMasks {
		if actual != mask {
			return false
		}
	}
	return true
}
