package aiservice

import "context"

type movementBattleRecoveryFunc func(context.Context) error

func (f movementBattleRecoveryFunc) Escape(ctx context.Context) error { return f(ctx) }
