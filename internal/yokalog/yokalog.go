package yokalog

import (
	"fmt"
	"path/filepath"
	"time"

	yokaman "gitlab.qa.dobest.com/qa/yokamancli"
	"go.uber.org/zap"
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
	err := yokaman.InitLogger(yokaman.LogConfig{
		Level:      cfg.Level,
		FileName:   cfg.FileName,
		MaxSize:    cfg.MaxSize,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAge,
	}, &level)
	if err != nil {
		return nil, nil, err
	}

	logger := zap.L()
	undoStdLog := zap.RedirectStdLog(logger)
	cleanup := func() {
		undoStdLog()
		_ = logger.Sync()
	}
	return logger, cleanup, nil
}
