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

type Config struct {
	Level      string
	FileName   string
	MaxSize    int
	MaxBackups int
	MaxAge     int
}

func DefaultConfig(service string) Config {
	return Config{
		Level:      "info",
		FileName:   filepath.Join(".", "logs", service, fmt.Sprintf("%s.log", time.Now().Format("20060102"))),
		MaxSize:    100,
		MaxBackups: 5,
		MaxAge:     30,
	}
}

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
