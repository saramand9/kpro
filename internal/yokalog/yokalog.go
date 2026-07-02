// File: internal/yokalog/yokalog.go
// Purpose: Defines or benchmarks the internal logging helper.

package yokalog

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Config controls zap and file-rotation logging.
type Config struct {
	// Level is the zap log level, for example "debug", "info", or "error".
	Level string
	// FileName is the log file path used by lumberjack rotation.
	FileName string
	// MaxSize is the per-file size limit in megabytes.
	MaxSize int
	// MaxBackups is the number of rotated files to keep.
	MaxBackups int
	// MaxAge is the number of days to keep rotated files.
	MaxAge int
}

// DefaultConfig returns logging defaults for a service name.
func DefaultConfig(service string) Config {
	return Config{
		Level:      "info",
		FileName:   filepath.Join(".", "logs", service, fmt.Sprintf("%s.log", time.Now().Format("20060102"))),
		MaxSize:    100,
		MaxBackups: 5,
		MaxAge:     30,
	}
}

// Init creates a zap logger, installs global redirects, and returns cleanup.
func Init(cfg Config) (*zap.Logger, func(), error) {
	if cfg.Level == "" {
		cfg.Level = "info"
	}
	if cfg.FileName == "" {
		cfg.FileName = DefaultConfig("kpro").FileName
	}
	if cfg.MaxSize <= 0 {
		cfg.MaxSize = 100
	}
	if cfg.MaxBackups <= 0 {
		cfg.MaxBackups = 5
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 30
	}

	level := zap.NewAtomicLevel()
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.FileName), 0o755); err != nil {
		return nil, nil, err
	}

	encoder := zapcore.NewJSONEncoder(zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	})
	writer := zapcore.AddSync(&lumberjack.Logger{
		Filename:   cfg.FileName,
		MaxSize:    cfg.MaxSize,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAge,
		Compress:   false,
	})

	core := zapcore.NewCore(encoder, writer, level)
	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	previous := zap.L()
	zap.ReplaceGlobals(logger)
	undoStdLog := zap.RedirectStdLog(logger)
	cleanup := func() {
		undoStdLog()
		zap.ReplaceGlobals(previous)
		_ = logger.Sync()
	}
	return logger, cleanup, nil
}
