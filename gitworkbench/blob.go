package gitworkbench

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

/**
 * [INPUT]: 依赖 Runner.Read（受限只读 Git）、一个 `<rev>:<path>` 形式的 blob 规格
 * [OUTPUT]: 对外提供 BlobSize（大小，不读内容）与 ReadBlob（带上限的 blob 字节），供 diff 卡片预览二进制/图片两侧
 * [POS]: gitworkbench 的 blob 只读入口；不新增命令类别，仅 cat-file -s / cat-file blob
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// BlobSize returns the byte size of the blob named by spec ("<rev>:<path>",
// ":0:<path>" for the index) without reading it.
func (r *Runner) BlobSize(ctx context.Context, root, spec string) (int64, error) {
	if spec == "" || strings.HasPrefix(spec, "-") {
		return 0, &Error{Class: ErrRefusal, OpClass: OpRead, Op: "cat-file", err: errors.New("not a blob spec")}
	}
	out, err := r.Read(ctx, root, "cat-file", "-s", spec)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

// ReadBlob returns the blob named by spec when it is at most max bytes; a
// larger one is refused with ErrTooLarge before any of it is read.
func (r *Runner) ReadBlob(ctx context.Context, root, spec string, max int64) ([]byte, error) {
	size, err := r.BlobSize(ctx, root, spec)
	if err != nil {
		return nil, err
	}
	if size > max {
		return nil, &Error{Class: ErrTooLarge, OpClass: OpRead, Op: "cat-file", err: errors.New("blob exceeds the preview limit")}
	}
	return r.Read(ctx, root, "cat-file", "blob", spec)
}
