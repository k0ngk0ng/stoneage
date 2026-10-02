`historical-single-mode.json` preserves the full single-mode model and Adam
reference used before mixed-mode PPO was added. Its canonical LearningState
SHA-256 is `47f04e2308d980abdbad5997c96777e65fe21aa1fd9c9ea7a697226355fa7a8b`.
The fixture was recovered on Darwin ARM64 with Go 1.26.2 only after verifying
that exact pre-existing digest. Tests verify the fixture digest and all shapes,
then compare every parameter and both optimizer moments numerically to allow
float32 rounding differences across compiler versions and CPU architectures.
This is a synthetic numerical regression fixture, not battle training data or
strategy-strength evidence. Do not regenerate it to accept algorithm changes.
