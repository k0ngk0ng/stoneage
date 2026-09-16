package airuntime

import "github.com/k0ngk0ng/stoneage/internal/characterbuild"

// CharacterBuild remains as a compatibility alias for the server-owned
// characterbuild.Policy type. New composition boundaries should use
// characterbuild.Policy directly.
type CharacterBuild = characterbuild.Policy

// AttributeWeights remains as a compatibility alias for characterbuild.Weights.
type AttributeWeights = characterbuild.Weights
