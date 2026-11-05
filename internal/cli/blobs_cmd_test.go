package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/objectstore"
)

// blobs 命令组的端到端用例（方案 15 §21）。这里锁的是「装配对不对」：配置面 → 平台库 →
// 后端三样接好之后，stat/get/put/gc 在真文件与真库上给出人看得懂的结果。回收语义本身
// 在 internal/service/blobgc_test.go 锁，两层不重复。

// blobsEnv 是一个最小现场：一个平台库 + 一个 dir 后端，体检链路完全不参与。
type blobsEnv struct {
	cfg   *config.Config
	root  string
	store string
	blobs string
}

func newBlobsEnv(t *testing.T, driver string) *blobsEnv {
	t.Helper()
	root := t.TempDir()
	env := &blobsEnv{
		cfg:   defaultCfg(t),
		root:  root,
		store: filepath.Join(root, "aegisops.db"),
		blobs: filepath.Join(root, "blobs"),
	}
	env.cfg.Platform.Store.Driver = config.StoreDriverSQLite
	env.cfg.Platform.Store.SQLitePath = env.store
	env.cfg.Platform.Blobs.Driver = driver
	env.cfg.Platform.Blobs.Dir = env.blobs
	return env
}

// newBlobsEnvWith 复用同一个平台库与同一份对象目录，只换 driver：演的是「配置切了、
// 对象没迁」这个运维现场，而不必真起一个 S3 端点（判定与 driver 无关）。
func newBlobsEnvWith(t *testing.T, src *blobsEnv, driver string) *blobsEnv {
	t.Helper()
	cfg := *src.cfg
	cfg.Platform.Blobs.Driver = driver
	return &blobsEnv{cfg: &cfg, root: src.root, store: src.store, blobs: src.blobs}
}

// run 执行一条命令，返回合并的 stdout+stderr（断言全是子串匹配，合起来更好读）。
func (e *blobsEnv) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd(testDeps(e.cfg, &fakeScan{}))
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err := root.Execute()
	combined := out.String()
	if errBuf.Len() > 0 {
		combined += errBuf.String()
	}
	return combined, err
}

// migrate 走真实入口建 schema：blobs 不替 aopsctl db migrate 动 schema，测试也不能绕。
func (e *blobsEnv) migrate(t *testing.T) {
	t.Helper()
	if out, err := e.run(t, "db", "migrate"); err != nil {
		t.Fatalf("db migrate: %v\n%s", err, out)
	}
}

// field 从 "key=abc size=3" 形式的输出里取一个字段。
func field(t *testing.T, out, name string) string {
	t.Helper()
	for _, line := range strings.Fields(out) {
		if rest, ok := strings.CutPrefix(line, name); ok {
			return rest
		}
	}
	t.Fatalf("field %q not found in output:\n%s", name, out)
	return ""
}

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestBlobsPutStatGetRoundTrip(t *testing.T) {
	env := newBlobsEnv(t, config.BlobDriverDir)
	env.migrate(t)

	src := writeTempFile(t, "report.md", "# cluster report\nweb pod crashlooping\n")
	out, err := env.run(t, "blobs", "put", src, "--scope", "c1")
	if err != nil {
		t.Fatalf("blobs put: %v\n%s", err, out)
	}
	key := field(t, out, "key=")
	if !strings.HasPrefix(key, "aegisops/reports/c1/") || !strings.HasSuffix(key, ".md") {
		t.Fatalf("unexpected key layout %q", key)
	}
	if !strings.Contains(out, "state=orphaned") {
		t.Fatalf("a backfilled object must be collectable until acquired:\n%s", out)
	}

	out, err = env.run(t, "blobs", "stat")
	if err != nil {
		t.Fatalf("blobs stat: %v\n%s", err, out)
	}
	if !strings.Contains(out, "driver=dir objects=1") || !strings.Contains(out, "total objects=1") {
		t.Fatalf("inventory not reported:\n%s", out)
	}

	out, err = env.run(t, "blobs", "stat", key)
	if err != nil {
		t.Fatalf("blobs stat key: %v\n%s", err, out)
	}
	for _, want := range []string{"category=reports", "driver=dir", "state=orphaned", "backend=dir  present"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stat %s missing %q:\n%s", key, want, out)
		}
	}

	out, err = env.run(t, "blobs", "get", key)
	if err != nil {
		t.Fatalf("blobs get: %v\n%s", err, out)
	}
	if !strings.Contains(out, "web pod crashlooping") {
		t.Fatalf("object content missing from stdout:\n%s", out)
	}

	dl := filepath.Join(t.TempDir(), "download.md")
	out, err = env.run(t, "blobs", "get", key, "-o", dl)
	if err != nil {
		t.Fatalf("blobs get -o: %v\n%s", err, out)
	}
	got, err := os.ReadFile(dl)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	if string(got) != "# cluster report\nweb pod crashlooping\n" {
		t.Fatalf("downloaded content = %q", got)
	}
}

func TestBlobsGCReclaimsOnlyAfterGrace(t *testing.T) {
	env := newBlobsEnv(t, config.BlobDriverDir)
	env.migrate(t)
	src := writeTempFile(t, "old.md", "candidate for collection\n")
	out, err := env.run(t, "blobs", "put", src, "--scope", "c1")
	if err != nil {
		t.Fatalf("blobs put: %v\n%s", err, out)
	}
	key := field(t, out, "key=")

	// 宽限期远未到期：--dry-run 与真跑都必须原封不动。
	out, err = env.run(t, "blobs", "gc", "--dry-run")
	if err != nil {
		t.Fatalf("gc dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "mode=dry-run") || !strings.Contains(out, "nothing to collect") {
		t.Fatalf("fresh object reported as collectable:\n%s", out)
	}

	time.Sleep(80 * time.Millisecond)

	out, err = env.run(t, "blobs", "gc", "--older-than", "1ms", "--dry-run")
	if err != nil {
		t.Fatalf("gc dry-run past grace: %v\n%s", err, out)
	}
	if !strings.Contains(out, "candidates=1") || !strings.Contains(out, "dry-run: no row and no object was touched") {
		t.Fatalf("dry-run did not report the candidate:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(env.blobs, filepath.FromSlash(key))); err != nil {
		t.Fatalf("dry run deleted content: %v", err)
	}

	out, err = env.run(t, "blobs", "gc", "--older-than", "1ms")
	if err != nil {
		t.Fatalf("gc: %v\n%s", err, out)
	}
	if !strings.Contains(out, "claimed=1") || !strings.Contains(out, "rows-removed=1") {
		t.Fatalf("gc accounting:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(env.blobs, filepath.FromSlash(key))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("collected object still on disk: %v", err)
	}
}

// TestBlobsRefusesToPretendUnmigratedObjectsAreGone 锁 §10.1 最贵的一句话：换了后端不等于
// 对象丢了。这里用 dir 写、按 db 读（同一条判定，且离线可跑），三条读路径都必须给迁移
// 指引——报「不存在」会让运维去翻备份，甚至让 verify 把好端端的对象判成缺失。
func TestBlobsRefusesToPretendUnmigratedObjectsAreGone(t *testing.T) {
	env := newBlobsEnv(t, config.BlobDriverDir)
	env.migrate(t)
	src := writeTempFile(t, "moved.md", "written while this deployment ran dir\n")
	out, err := env.run(t, "blobs", "put", src, "--scope", "c1")
	if err != nil {
		t.Fatalf("blobs put: %v\n%s", err, out)
	}
	key := field(t, out, "key=")

	switched := newBlobsEnvWith(t, env, config.BlobDriverDB)

	out, err = switched.run(t, "blobs", "get", key)
	if err == nil {
		t.Fatalf("reading an un-migrated object must fail, got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "blobs migrate --to db") {
		t.Fatalf("get must name the migration command: %v", err)
	}
	if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no objects row") {
		t.Fatalf("an un-migrated object must never read as missing: %v", err)
	}

	out, err = switched.run(t, "blobs", "stat", key)
	if err != nil {
		t.Fatalf("stat after the switch: %v\n%s", err, out)
	}
	if !strings.Contains(out, "backend=other") || !strings.Contains(out, "not a loss") {
		t.Fatalf("stat must say the content is on another driver:\n%s", out)
	}
	if strings.Contains(out, "MISSING") {
		t.Fatalf("stat must not call an un-migrated object MISSING:\n%s", out)
	}

	out, err = switched.run(t, "blobs", "stat")
	if err != nil {
		t.Fatalf("inventory after the switch: %v\n%s", err, out)
	}
	if !strings.Contains(out, `content lives on "dir", run: aopsctl blobs migrate --to db`) {
		t.Fatalf("inventory must point at the migration command:\n%s", out)
	}

	// GC 也不能拿它开刀：抢在 migrate 前面把这一行忘掉，等于擦掉唯一的迁移线索。
	time.Sleep(80 * time.Millisecond)
	out, err = switched.run(t, "blobs", "gc", "--older-than", "1ms")
	if err != nil {
		t.Fatalf("gc after the switch: %v\n%s", err, out)
	}
	if !strings.Contains(out, "skipped-foreign=1") || !strings.Contains(out, "blobs migrate --to db") {
		t.Fatalf("gc must skip and name foreign-driver candidates:\n%s", out)
	}
	if !strings.Contains(out, "rows-removed=0") {
		t.Fatalf("gc must not forget rows it cannot clean up after:\n%s", out)
	}
	// 原后端里的内容一个字节都没动。
	if _, err := os.Stat(filepath.Join(env.blobs, filepath.FromSlash(key))); err != nil {
		t.Fatalf("the switch must not have touched the source object: %v", err)
	}
}

// TestBlobsSurvivesWholeDirectoryRelocation 是 §21 的 CLI 端到端：整个 ~/.aegisops 目录
// （平台库 + dir 后端）搬到新位置之后，对象照样列得出、读得出。这是单机 appliance 的
// 基本盘，也是「不破坏 CLI 现有行为」这条约束的具体形状。
func TestBlobsSurvivesWholeDirectoryRelocation(t *testing.T) {
	env := newBlobsEnv(t, config.BlobDriverDir)
	env.migrate(t)
	src := writeTempFile(t, "portable.md", "single node appliance backup\n")
	out, err := env.run(t, "blobs", "put", src, "--scope", "c1")
	if err != nil {
		t.Fatalf("blobs put: %v\n%s", err, out)
	}
	key := field(t, out, "key=")

	// 整目录搬迁：库文件和对象目录一起走，配置只改两个路径。
	dst := filepath.Join(t.TempDir(), "k8s-ai")
	if err := os.Rename(env.root, dst); err != nil {
		t.Fatalf("relocate home directory: %v", err)
	}
	env.cfg.Platform.Store.SQLitePath = filepath.Join(dst, "aegisops.db")
	env.cfg.Platform.Blobs.Dir = filepath.Join(dst, "blobs")
	env.blobs = filepath.Join(dst, "blobs")

	out, err = env.run(t, "blobs", "stat")
	if err != nil {
		t.Fatalf("stat after relocation: %v\n%s", err, out)
	}
	if !strings.Contains(out, "total objects=1") {
		t.Fatalf("inventory lost after relocation:\n%s", out)
	}
	out, err = env.run(t, "blobs", "get", key)
	if err != nil {
		t.Fatalf("get after relocation: %v\n%s", err, out)
	}
	if !strings.Contains(out, "single node appliance backup") {
		t.Fatalf("object unreadable after relocation:\n%s", out)
	}
}

// TestBlobsAnswersWithoutABackend 锁 off 的三态：观察类子命令照常答并说清原因，
// 写入与下载类子命令拒绝且把配置键名写在错误里（方案 15 §16.2）。
func TestBlobsAnswersWithoutABackend(t *testing.T) {
	env := newBlobsEnv(t, config.BlobDriverOff)
	env.migrate(t)

	out, err := env.run(t, "blobs", "stat")
	if err != nil {
		t.Fatalf("stat with driver=off must not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "platform.blobs.driver=off") {
		t.Fatalf("stat must name the reason instead of guessing:\n%s", out)
	}

	out, err = env.run(t, "blobs", "gc")
	if err != nil {
		t.Fatalf("gc with driver=off must not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to collect") {
		t.Fatalf("gc must say there is nothing to collect:\n%s", out)
	}

	writes := [][]string{
		{"blobs", "get", "aegisops/reports/c1/" + strings.Repeat("a", objectstore.SHA256HexLen) + ".md"},
		{"blobs", "put", writeTempFile(t, "x.md", "content\n"), "--scope", "c1"},
	}
	for _, args := range writes {
		out, err := env.run(t, args...)
		if err == nil {
			t.Fatalf("%v must be refused with driver=off:\n%s", args, out)
		}
		if !strings.Contains(err.Error(), "driver=off") {
			t.Fatalf("%v must name the config key, got %v", args, err)
		}
	}
}

// TestBlobsRefusesUnmigratedPlatformSchema 锁装配顺序：objects 表还没建就先读版本，
// 报一句「跑 aopsctl db migrate」，而不是把统计跑成空表或一句 SQL 错误。
func TestBlobsRefusesUnmigratedPlatformSchema(t *testing.T) {
	env := newBlobsEnv(t, config.BlobDriverDir)

	out, err := env.run(t, "blobs", "stat")
	if err == nil {
		t.Fatalf("blobs must refuse a schema without the objects table:\n%s", out)
	}
	if !strings.Contains(err.Error(), "db migrate") {
		t.Fatalf("error must point at the migrate entrypoint, got %v", err)
	}
	// 报错必须指名实际打开的库：漏 --config 时打开的是默认 sqlite 空库，只喊「去 db
	// migrate」会把人引向错方向（2026-11-03 真机踩过一次）。
	if !strings.Contains(err.Error(), "store sqlite:") {
		t.Fatalf("error must name the store actually opened, got %v", err)
	}
	if !strings.Contains(err.Error(), "--config") {
		t.Fatalf("error must hint that the config file decides the store, got %v", err)
	}
	if strings.Contains(out, "total objects=") {
		t.Fatalf("must not report inventory from an unmigrated schema:\n%s", out)
	}
}

// TestBlobsVerifyGatesTheRestoreRun 锁 `blobs verify` 作为恢复准出门禁的形状：干净态一句话
// 过；内容真丢时 --strict 必须非零退出并给出 key；后端切了但对象没迁只报待迁移、不报不一致。
func TestBlobsVerifyGatesTheRestoreRun(t *testing.T) {
	env := newBlobsEnv(t, config.BlobDriverDir)
	env.migrate(t)
	src := writeTempFile(t, "clean.md", "restore gate content\n")
	out, err := env.run(t, "blobs", "put", src, "--scope", "c1")
	if err != nil {
		t.Fatalf("blobs put: %v\n%s", err, out)
	}
	key := field(t, out, "key=")

	out, err = env.run(t, "blobs", "verify")
	if err != nil {
		t.Fatalf("verify on a clean store must not fail: %v\n%s", err, out)
	}
	for _, want := range []string{"driver=dir mode=stat category=all", "checked=1 ok=1", "orphans=0", "consistent"} {
		if !strings.Contains(out, want) {
			t.Fatalf("verify output missing %q:\n%s", want, out)
		}
	}

	out, err = env.run(t, "blobs", "verify", "--deep")
	if err != nil {
		t.Fatalf("deep verify: %v\n%s", err, out)
	}
	if !strings.Contains(out, "mode=deep") || !strings.Contains(out, "deep-verified=1") {
		t.Fatalf("deep mode must actually recompute the digest:\n%s", out)
	}

	// 内容被删（恢复点错位 / 桶侧被清理）：--strict 要把这轮变成非零退出。
	if err := os.Remove(filepath.Join(env.blobs, filepath.FromSlash(key))); err != nil {
		t.Fatalf("remove content: %v", err)
	}
	out, err = env.run(t, "blobs", "verify", "--strict")
	if err == nil {
		t.Fatalf("--strict must fail once content is gone:\n%s", out)
	}
	if !strings.Contains(out, "INCONSISTENT") || !strings.Contains(out, "missing: "+key) {
		t.Fatalf("verify must name the dangling key:\n%s", out)
	}
	if !strings.Contains(out, "state=missing") {
		t.Fatalf("verify must record the diagnosis in the registry:\n%s", out)
	}
}

// TestBlobsVerifyReportsUnmigratedRowsAsPending 是 §10.1 在对账这一侧的形状：换后端之后
// 第一轮 verify 绝不能把待迁移对象读成数据丢失，否则运维会照着假结论去翻备份。
func TestBlobsVerifyReportsUnmigratedRowsAsPending(t *testing.T) {
	env := newBlobsEnv(t, config.BlobDriverDir)
	env.migrate(t)
	src := writeTempFile(t, "pending.md", "written on the old driver\n")
	out, err := env.run(t, "blobs", "put", src, "--scope", "c1")
	if err != nil {
		t.Fatalf("blobs put: %v\n%s", err, out)
	}
	key := field(t, out, "key=")

	switched := newBlobsEnvWith(t, env, config.BlobDriverDB)
	out, err = switched.run(t, "blobs", "verify", "--strict")
	if err != nil {
		t.Fatalf("pending migration is not an inconsistency: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pending:   1 row(s) still on another driver") {
		t.Fatalf("un-migrated row not reported as pending:\n%s", out)
	}
	if !strings.Contains(out, "blobs migrate --to db") {
		t.Fatalf("verify must name the migration command:\n%s", out)
	}
	if strings.Contains(out, "INCONSISTENT") || !strings.Contains(out, "missing=0") {
		t.Fatalf("objects on another driver must never read as a loss:\n%s", out)
	}
	// 对账只读不写：源后端里的字节必须一个都没动。
	if _, err := os.Stat(filepath.Join(env.blobs, filepath.FromSlash(key))); err != nil {
		t.Fatalf("verify touched the source object: %v", err)
	}
}

// TestBlobsVerifyWithoutABackend 锁 off 态：不假装扫干净，而是说清「没有后端可对」，
// 并把表里还在等后端的行数报出来（方案 15 §16.2）。
func TestBlobsVerifyWithoutABackend(t *testing.T) {
	env := newBlobsEnv(t, config.BlobDriverOff)
	env.migrate(t)

	out, err := env.run(t, "blobs", "verify")
	if err != nil {
		t.Fatalf("verify with driver=off must not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no content backend to verify against") {
		t.Fatalf("verify must name the reason:\n%s", out)
	}
}

// TestBlobsMigrateRequiresATarget 锁 `blobs migrate` 的入口闸门（方案 15 §10.1）：目标必须由运维
// 显式给出、必须是真后端、必须与当前后端不同，目标后端构造失败时还要把配置键名说出口。
// 迁移工具指错目标端比迁不动更危险——它会把对象写到一个没人备份的地方。
func TestBlobsMigrateRequiresATarget(t *testing.T) {
	env := newBlobsEnv(t, config.BlobDriverDir)
	env.migrate(t)

	if out, err := env.run(t, "blobs", "migrate"); err == nil {
		t.Fatalf("migrate must refuse to guess a target:\n%s", out)
	} else if !strings.Contains(err.Error(), `required flag(s) "to"`) {
		t.Fatalf("a missing --to must stay a flag error, got %v", err)
	}

	// off 不是一个存放地点：迁过去等于把对象扔出系统。
	if _, err := env.run(t, "blobs", "migrate", "--to", "off"); err == nil ||
		!strings.Contains(err.Error(), "off is not a place") {
		t.Fatalf("off must be refused as a target: %v", err)
	}

	// 目标就是当前后端：这不是错误，是一句「没什么可迁」，别让人以为工具坏了。
	out, err := env.run(t, "blobs", "migrate", "--to", "dir")
	if err != nil {
		t.Fatalf("migrating onto the current driver is a no-op, not a failure: %v\n%s", err, out)
	}
	if !strings.Contains(out, `already "dir": nothing to migrate`) {
		t.Fatalf("the no-op must say so plainly:\n%s", out)
	}

	// s3 目标缺 endpoint：构造期就拒，并指向配置键。跑到一半才发现配错，代价是半批对象
	// 落在一个谁都找不到的地方。
	if _, err := env.run(t, "blobs", "migrate", "--to", "s3"); err == nil ||
		!strings.Contains(err.Error(), "target backend") ||
		!strings.Contains(err.Error(), "platform.blobs.endpoint") {
		t.Fatalf("an unusable s3 target must be refused with its config key: %v", err)
	}

	// 源端永远是配置里那一个：driver=off 时没有源，不能假装从"无"里搬对象。
	off := newBlobsEnv(t, config.BlobDriverOff)
	off.migrate(t)
	if _, err := off.run(t, "blobs", "migrate", "--to", "db"); err == nil ||
		!strings.Contains(err.Error(), "driver=off") {
		t.Fatalf("migrate with driver=off must name the config key: %v", err)
	}
}

// TestBlobsMigrateMovesObjectsOutOfTheDatabase 是 §10.1 的 CLI 端到端：db 后端写下的对象靠一条
// 命令搬到 dir，配置才敢切过去。搬运规则本身在 internal/service/blobmigrate_test.go 锁，这里只锁
// 「运维按文案操作能不能走通」：dry-run 零改动、真搬之后切配置读得出、对账不再报待迁移。
func TestBlobsMigrateMovesObjectsOutOfTheDatabase(t *testing.T) {
	env := newBlobsEnv(t, config.BlobDriverDB)
	env.migrate(t)
	src := writeTempFile(t, "settle.md", "written while this deployment kept content in the database\n")
	out, err := env.run(t, "blobs", "put", src, "--scope", "c1")
	if err != nil {
		t.Fatalf("blobs put: %v\n%s", err, out)
	}
	key := field(t, out, "key=")
	object := filepath.Join(env.blobs, filepath.FromSlash(key))

	out, err = env.run(t, "blobs", "migrate", "--to", "dir", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run migrate: %v\n%s", err, out)
	}
	for _, want := range []string{"db -> dir mode=dry-run", "candidates=1", "flipped=0",
		"dry-run: no object and no row was touched"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(object); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote content to the target: %v", err)
	}

	out, err = env.run(t, "blobs", "migrate", "--to", "dir", "--prune-source")
	if err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	for _, want := range []string{"copied=1", "flipped=1", "pruned=1", `remaining: none on "db"`, "is now safe"} {
		if !strings.Contains(out, want) {
			t.Fatalf("migrate output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(object); err != nil {
		t.Fatalf("the object never reached the target: %v", err)
	}

	// 配置还没切：旧后端读不到正文，但绝不能报成「登记表里没这一行」——那会把运维送去翻备份，
	// 而正确答案只是「把 platform.blobs.driver 改成 dir」。
	if _, err := env.run(t, "blobs", "get", key); err == nil {
		t.Fatalf("reading a migrated object through the old driver must fail")
	} else if strings.Contains(err.Error(), "no objects row") {
		t.Fatalf("the registry row still exists, do not report it gone: %v", err)
	}

	// 切配置之后：读得出来、对账干净、存量算在对的后端名下。
	switched := newBlobsEnvWith(t, env, config.BlobDriverDir)
	out, err = switched.run(t, "blobs", "get", key)
	if err != nil {
		t.Fatalf("get after the switch: %v\n%s", err, out)
	}
	if !strings.Contains(out, "kept content in the database") {
		t.Fatalf("migrated content unreadable:\n%s", out)
	}

	out, err = switched.run(t, "blobs", "verify", "--strict")
	if err != nil {
		t.Fatalf("verify after a finished migration: %v\n%s", err, out)
	}
	if !strings.Contains(out, "consistent") || strings.Contains(out, "pending") {
		t.Fatalf("a finished migration must leave nothing pending:\n%s", out)
	}

	out, err = switched.run(t, "blobs", "stat")
	if err != nil {
		t.Fatalf("stat after the switch: %v\n%s", err, out)
	}
	if !strings.Contains(out, "driver=dir objects=1") {
		t.Fatalf("the inventory must count the object on the new driver:\n%s", out)
	}
}
