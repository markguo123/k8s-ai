package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// P8.10 的语义锁（方案 15 §12.1、§12.2）。这批用例盯的是同一件事：**对账没有把握时不许
// 下结论**。误报「对象丢了」比漏报危险得多——它会让运维从备份里删掉那份还好好躺在桶里的
// 内容，或者干脆重跑一次体检把历史报告的字节换掉。
//
// 后端一律用真 dir：missing/unavailable/静默损坏三种情形，只有真文件系统的行为才作数。

// verifyDirPath 复刻 dir 后端的落盘布局，用于直接改动或删掉正文。
func verifyDirPath(t *testing.T, root, key string) string {
	t.Helper()
	return filepath.Join(root, filepath.FromSlash(key))
}

func verifyRun(t *testing.T, backend objectstore.Store, st store.Store, opts VerifyOptions) VerifyResult {
	t.Helper()
	verifier, err := NewBlobVerifier(backend, st.Objects())
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	res, err := verifier.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return res
}

func TestBlobVerifyReportsConsistentRegistry(t *testing.T) {
	st := newArchiveStore(t)
	backend := gcDirBackend(t)
	refA := gcPutObject(t, st, backend, "alpha crashlooping")
	gcPutObject(t, st, backend, "beta pending")

	res := verifyRun(t, backend, st, VerifyOptions{Prefix: objectstore.DefaultPrefix})
	if res.Rows != 2 || res.OK != 2 || res.Inconsistent() != 0 {
		t.Fatalf("clean backend must verify clean: %+v", res)
	}
	if res.BucketKeys != 2 || res.Orphans != 0 || res.ForeignDriver != 0 {
		t.Fatalf("reverse scan miscounted: %+v", res)
	}
	if !gcReadable(t, backend, refA.Key) {
		t.Fatal("verify must never touch content")
	}
}

func TestBlobVerifyMarksMissingOnlyWhenTheBackendAnswered(t *testing.T) {
	root := t.TempDir()
	st := newArchiveStore(t)
	backend := gcDirBackendAt(t, root)
	live := gcPutObject(t, st, backend, "still here")
	lost := gcPutObject(t, st, backend, "content removed behind our back")
	if err := os.Remove(verifyDirPath(t, root, lost.Key)); err != nil {
		t.Fatalf("remove content: %v", err)
	}

	res := verifyRun(t, backend, st, VerifyOptions{Prefix: objectstore.DefaultPrefix})
	if res.Missing != 1 || res.OK != 1 || res.RowsMarkedMissing != 1 {
		t.Fatalf("one object was removed: %+v", res)
	}
	if len(res.MissingKeys) != 1 || res.MissingKeys[0] != lost.Key {
		t.Fatalf("missing sample = %v, want %s", res.MissingKeys, lost.Key)
	}
	if res.Inconsistent() != 1 {
		t.Fatalf("a dangling reference must count as inconsistent: %+v", res)
	}
	if got := gcMeta(t, st, lost.Key); got.State != store.ObjectStateMissing {
		t.Errorf("confirmed-missing row = %s, want missing (the download path needs the 410)", got.State)
	}
	if got := gcMeta(t, st, live.Key); got.State != store.ObjectStateReady {
		t.Errorf("healthy row was dragged to %s", got.State)
	}
}

// verifyUnavailable 只把 Stat 变成「后端不可达」，其余照旧。
type verifyUnavailable struct{ objectstore.Store }

func (verifyUnavailable) Stat(context.Context, string) (objectstore.Ref, error) {
	return objectstore.Ref{}, fmt.Errorf("%w: simulated head-object timeout", objectstore.ErrUnavailable)
}

// TestBlobVerifyAbortsInsteadOfGuessingWhenBackendIsUnreachable 是 §12.2 那条硬规矩的
// 可执行版本：一次网络抖动没有资格把健康对象判成 missing。
func TestBlobVerifyAbortsInsteadOfGuessingWhenBackendIsUnreachable(t *testing.T) {
	st := newArchiveStore(t)
	backend := gcDirBackend(t)
	kept := gcPutObject(t, st, backend, "healthy object")

	verifier, err := NewBlobVerifier(verifyUnavailable{backend}, st.Objects())
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	_, err = verifier.Run(context.Background(), VerifyOptions{Prefix: objectstore.DefaultPrefix})
	if !errors.Is(err, objectstore.ErrUnavailable) {
		t.Fatalf("unreachable backend = %v, want ErrUnavailable (abort, no verdict)", err)
	}
	if got := gcMeta(t, st, kept.Key); got.State != store.ObjectStateReady {
		t.Fatalf("outage sentenced a healthy row to %s", got.State)
	}
}

// TestBlobVerifyDeepCatchesSilentCorruption 解释了两档模式为什么都要存在：篡改者把字节
// 换成同长度的内容，size 校验完全看不出来。
func TestBlobVerifyDeepCatchesSilentCorruption(t *testing.T) {
	root := t.TempDir()
	st := newArchiveStore(t)
	backend := gcDirBackendAt(t, root)
	ref := gcPutObject(t, st, backend, "aaaaaaaa")
	if err := os.WriteFile(verifyDirPath(t, root, ref.Key), []byte("bbbbbbbb"), 0o600); err != nil {
		t.Fatalf("corrupt content: %v", err)
	}

	shallow := verifyRun(t, backend, st, VerifyOptions{Prefix: objectstore.DefaultPrefix})
	if shallow.OK != 1 || shallow.Inconsistent() != 0 {
		t.Fatalf("size-only mode cannot see same-length corruption, got %+v", shallow)
	}

	deep := verifyRun(t, backend, st, VerifyOptions{Prefix: objectstore.DefaultPrefix, Deep: true})
	if deep.DeepChecked != 1 || deep.ChecksumMismatch != 1 || deep.Inconsistent() != 1 {
		t.Fatalf("deep mode must catch it: %+v", deep)
	}
	if len(deep.ChecksumMismatchKeys) != 1 || deep.ChecksumMismatchKeys[0] != ref.Key {
		t.Fatalf("checksum sample = %v", deep.ChecksumMismatchKeys)
	}
	if deep.RowsMarkedMissing != 0 {
		t.Fatal("corruption is not missing: the bytes are there, marking it missing would hide the real problem")
	}
}

func TestBlobVerifySizeMismatchAndLyingRow(t *testing.T) {
	root := t.TempDir()
	st := newArchiveStore(t)
	backend := gcDirBackendAt(t, root)
	ref := gcPutObject(t, st, backend, "registered size is a lie")

	wrong := gcMeta(t, st, ref.Key)
	wrong.Size = ref.Size + 10
	if err := st.Objects().Register(context.Background(), wrong); err != nil {
		t.Fatalf("register wrong size: %v", err)
	}

	res := verifyRun(t, backend, st, VerifyOptions{Prefix: objectstore.DefaultPrefix})
	if res.SizeMismatch != 1 || res.OK != 0 || res.Inconsistent() != 1 {
		t.Fatalf("size mismatch not reported: %+v", res)
	}
	// 登记摘要与 key 里的摘要不符 = 这行不是本包写的，内容判定对它没有意义。
	liarRef := gcPutObject(t, st, backend, "row that does not match its own key")
	liar := gcMeta(t, st, liarRef.Key)
	liar.SHA256 = strings.Repeat("0", objectstore.SHA256HexLen)
	if err := st.Objects().Register(context.Background(), liar); err != nil {
		t.Fatalf("register lying row: %v", err)
	}
	res = verifyRun(t, backend, st, VerifyOptions{Prefix: objectstore.DefaultPrefix})
	if res.AddressMismatch != 1 {
		t.Fatalf("address mismatch not reported: %+v", res)
	}
}

func TestBlobVerifyLeavesUnmigratedRowsToMigrate(t *testing.T) {
	st := newArchiveStore(t)
	backend := gcDirBackend(t)
	ours := gcPutObject(t, st, backend, "on this driver")

	// 演「配置切了、对象没迁」：这一行在 dir 后端里当然找不到内容。
	foreign := gcMeta(t, st, ours.Key)
	foreign.BlobKey = strings.Replace(foreign.BlobKey, "/c1/", "/c2/", 1)
	foreign.Driver = objectstore.DriverS3
	if err := st.Objects().Register(context.Background(), foreign); err != nil {
		t.Fatalf("register foreign row: %v", err)
	}

	res := verifyRun(t, backend, st, VerifyOptions{Prefix: objectstore.DefaultPrefix})
	if res.ForeignDriver != 1 {
		t.Fatalf("unmigrated row not counted as pending: %+v", res)
	}
	if res.Missing != 0 || res.Inconsistent() != 0 {
		t.Fatalf("content living on another driver is not a loss: %+v", res)
	}
}

// TestBlobVerifyReverseScanAndRowsInTheVoid 锁两件事：桶有内容而表里没行 = 待回收孤儿；
// 正在 GC 的行不判缺失。两者都来自「表是唯一依据」这条口径（§9.3 #4）。
func TestBlobVerifyReverseScanAndRowsInTheVoid(t *testing.T) {
	root := t.TempDir()
	st := newArchiveStore(t)
	backend := gcDirBackendAt(t, root)
	kept := gcPutObject(t, st, backend, "referenced object")

	// 邻居：内容写进桶但从没登记（例如上一轮 Put 之后进程被杀）。
	if _, err := backend.Put(context.Background(), objectstore.Spec{
		Category:  objectstore.CategoryReports,
		Scope:     []string{"c9"},
		Ext:       ".md",
		CreatedBy: objectstore.SystemActor,
	}, strings.NewReader("never registered")); err != nil {
		t.Fatalf("put unregistered object: %v", err)
	}
	// 正在回收的行：内容已经删掉了，但这是 GC 的正常中间态。
	// 用真实的 GC 前半段造这个状态：引用归零 -> 过宽限期 -> 两段式认领。
	// （Register 不覆盖 state，绕道写 state 只会测到一个 DB 根本不允许的形态。）
	doomed := gcPutObject(t, st, backend, "collected right now")
	gcOrphan(t, st, doomed.Key)
	gcWaitPastGrace()
	claimed, err := st.Objects().ClaimDeleting(context.Background(), doomed.Key, 0, time.Now())
	if err != nil {
		t.Fatalf("claim %s deleting: %v", doomed.Key, err)
	}
	if !claimed {
		t.Fatal("the row must be claimable at this point")
	}
	if err := os.Remove(verifyDirPath(t, root, doomed.Key)); err != nil {
		t.Fatalf("remove content: %v", err)
	}

	res := verifyRun(t, backend, st, VerifyOptions{Prefix: objectstore.DefaultPrefix})
	if res.Deleting != 1 || res.Missing != 0 {
		t.Fatalf("a row mid garbage collection is not corruption: %+v", res)
	}
	if res.Orphans != 1 || res.OrphanBytes == 0 {
		t.Fatalf("unregistered content must surface as an orphan: %+v", res)
	}
	if res.Inconsistent() != 1 {
		t.Fatalf("--strict must fail on orphans too (§12.2): %+v", res)
	}
	if !gcReadable(t, backend, kept.Key) {
		t.Fatal("verify deleted something")
	}
}

func TestBlobVerifierRejectsBadInput(t *testing.T) {
	st := newArchiveStore(t)
	backend := gcDirBackend(t)
	verifier, err := NewBlobVerifier(backend, st.Objects())
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	if _, err := verifier.Run(context.Background(), VerifyOptions{Category: "secrets"}); err == nil {
		t.Fatal("an unknown category must be refused, not scanned as empty")
	}
	if _, err := verifier.Run(context.Background(), VerifyOptions{Prefix: "../etc"}); err == nil {
		t.Fatal("an unsafe prefix must be refused")
	}
	if _, err := NewBlobVerifier(nil, st.Objects()); err == nil {
		t.Fatal("verifier without a backend must fail at construction")
	}
}
