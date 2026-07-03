// File: platform/logx/logx_bench_test.go
// Purpose: Benchmarks the public KPro logging foundation package.

package logx

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func benchmarkLogger(b *testing.B) *zap.Logger {
	b.Helper()

	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		_ = devNull.Close()
	})

	oldStdout := os.Stdout
	os.Stdout = devNull
	b.Cleanup(func() {
		os.Stdout = oldStdout
	})

	logDir := filepath.Join(os.TempDir(), "kpro-logx-bench")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		b.Fatal(err)
	}

	logger, cleanup, err := Init(Config{
		Level:      "info",
		FileName:   filepath.Join(logDir, "bench.log"),
		MaxSize:    1024,
		MaxBackups: 1,
		MaxAge:     1,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(cleanup)
	return logger
}

func BenchmarkInfoNoFields(b *testing.B) {
	logger := benchmarkLogger(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("kpro request handled")
	}
}

func BenchmarkInfoWithFields(b *testing.B) {
	logger := benchmarkLogger(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("kpro request handled",
			zap.String("transport", "tcp"),
			zap.Uint64("seq", uint64(i)),
			zap.Int("bytes", 64),
		)
	}
}

func BenchmarkDebugDisabled(b *testing.B) {
	logger := benchmarkLogger(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Debug("kpro request handled",
			zap.String("transport", "tcp"),
			zap.Uint64("seq", uint64(i)),
			zap.Int("bytes", 64),
		)
	}
}
