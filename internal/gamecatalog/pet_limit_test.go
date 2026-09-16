package gamecatalog

import (
	"strings"
	"testing"
)

func TestPetLimitUsesNativeTemplateColumn(t *testing.T) {
	fields := make([]string, 55)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "test-pet"
	fields[6] = "42"
	fields[36] = "100250"
	fields[53] = "999"
	fields[54] = "80"
	pets, err := ParsePets(strings.NewReader(strings.Join(fields, ",") + "\n"))
	if err != nil || len(pets) != 1 || pets[0].LimitLevel != 80 || pets[0].TemplateID != 42 {
		t.Fatalf("native limit column: %+v %v", pets, err)
	}
}
