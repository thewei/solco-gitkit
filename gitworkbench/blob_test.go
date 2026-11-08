package gitworkbench

import (
	"context"
	"strings"
	"testing"
)

/**
 * [INPUT]: 真实临时 Git 仓库里已提交与已暂存的二进制 blob
 * [OUTPUT]: BlobSize/ReadBlob 的字节、上限拒绝、非法规格拒绝回归
 * [POS]: blob.go 的真实 Git 回归
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

func TestReadBlobBoundsAndRefuses(t *testing.T) {
	root, r := tempRepo(t)
	writeRepoFile(t, root, "a.bin", "\x00\x01\x02\x03")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-q", "-m", "base")
	writeRepoFile(t, root, "a.bin", "\x00\x01\x02\x03\x04\x05")
	runGit(t, root, "add", ".")

	ctx := context.Background()
	if n, err := r.BlobSize(ctx, root, "HEAD:a.bin"); err != nil || n != 4 {
		t.Fatalf("HEAD size = %d, %v; want 4", n, err)
	}
	if n, err := r.BlobSize(ctx, root, ":0:a.bin"); err != nil || n != 6 {
		t.Fatalf("index size = %d, %v; want 6", n, err)
	}
	if data, err := r.ReadBlob(ctx, root, "HEAD:a.bin", 16); err != nil || string(data) != "\x00\x01\x02\x03" {
		t.Fatalf("ReadBlob = %q, %v", data, err)
	}
	if _, err := r.ReadBlob(ctx, root, ":0:a.bin", 5); err == nil || !strings.Contains(err.Error(), "preview limit") {
		t.Fatalf("a blob over the limit must be refused, got %v", err)
	}
	if _, err := r.BlobSize(ctx, root, "--help"); err == nil {
		t.Fatal("an option-shaped spec must be refused")
	}
	if _, err := r.BlobSize(ctx, root, "HEAD:missing.bin"); err == nil {
		t.Fatal("a missing blob is an error")
	}
}
