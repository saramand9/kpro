package zapx

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func newObservedLogger(core zapcore.Core) *zap.Logger {
	return zap.New(core)
}
