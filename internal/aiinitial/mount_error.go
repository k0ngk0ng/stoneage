package aiinitial

import "errors"

// ErrInvalidMount is safe to classify at the HTTP boundary without exposing
// native responses, account identifiers or bridge paths.
var ErrInvalidMount = errors.New("首只宠物无法作为初始坐骑，请检查宠物种类和人物、宠物等级范围，或关闭默认骑乘")
