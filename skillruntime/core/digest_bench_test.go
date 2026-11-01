package core

import (
	"crypto/sha256"
	"io"
	"os"
	"testing"
)

func BenchmarkNativeBundleDigestIO(b *testing.B) {
	path := os.Getenv("POGO_SKILL_RUNTIME_BENCH_ARCHIVE")
	if path == "" {
		b.Skip("set POGO_SKILL_RUNTIME_BENCH_ARCHIVE to a prebuilt native bundle")
	}
	f, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	info, _ := f.Stat()
	for _, bounded := range []bool{false, true} {
		name := "default-buffer"
		if bounded {
			name = "pooled-1MiB"
		}
		b.Run(name, func(b *testing.B) {
			b.SetBytes(info.Size())
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if bounded {
					if _, _, err := digestFile(f, MaxOuterArchiveBytes); err != nil {
						b.Fatal(err)
					}
				} else {
					if _, err := io.Copy(sha256.New(), io.NewSectionReader(f, 0, info.Size()+1)); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
