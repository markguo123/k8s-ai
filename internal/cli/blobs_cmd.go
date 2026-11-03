package cli

// 本文件是 k8s-ai blobs 命令组（P8.9 / 方案 15 §9.3、§12.2、§10.1）。它和上面那份
// server 装配（blobs.go: newBlobGateway）刻意是两套：网关的存在意义是「降级继续服务」，
// 而运维命令没有降级可言——后端连不上就必须报错退出，把 503 打在终端上只会让人以为命令
// 写坏了。共用的是同一批原语（objectstore.New、store.Objects()、service 的回收器），
// 所以两边看到的对象世界完全一致。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// errBlobBackendUnavailable 是「命令需要一个能用的后端，但此刻没有」的统一说法。off 与
// 就绪失败都归到这里，文案分别由调用点补：前者是「这个功能没开」，后者是「后端坏了」。
var errBlobBackendUnavailable = errors.New("object storage backend is not usable")

// newBlobsCmd 聚合对象存储的运维入口。
//
// 它和 k8s-ai db 一样属「显式运维命令」：需要平台库，也允许 driver=s3。§16 的 CLI 零影响
// 约束管的是巡检链路（scan/chat）不得因为一个桶起不来，不是禁止运维工具碰桶——迁移动词
// 天生要 s3 凭据（§10.1）。
func newBlobsCmd(deps Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "blobs",
		Short: "Object storage operations (inspect, download, garbage collect)",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(
		newBlobsStatCmd(deps), newBlobsGetCmd(deps), newBlobsPutCmd(deps),
		newBlobsGCCmd(deps), newBlobsVerifyCmd(deps), newBlobsMigrateCmd(deps),
	)
	return cmd
}

// blobOps 是一次命令的装配结果。Close 必须调用：dir 后端持着根目录独占锁。
type blobOps struct {
	cfg     *config.Config
	st      store.Store
	backend objectstore.Store
	gc      *service.BlobGC
	driver  string
	prefix  string
	// maxBytes 是 platform.blobs.max_object_size 解析后的体积闸门，迁移要拿它再构造
	// 第二个后端（两个后端的闸门必须一致，否则同一份内容在一端能写、在另一端超）。
	maxBytes int64
	// disabled 记录 driver=off：观察类子命令照常答（表是活的），写入类子命令拒绝。
	disabled bool
}

func (o *blobOps) Close() {
	if o == nil {
		return
	}
	if o.backend != nil {
		_ = o.backend.Close()
	}
	if o.st != nil {
		_ = o.st.Close()
	}
}

// requireBackend 给写入/下载类子命令把关：off 与未就绪都在这里变成一句人话。
func (o *blobOps) requireBackend() error {
	if o.disabled {
		return fmt.Errorf("%w: platform.blobs.driver=off, object storage is not enabled", objectstore.ErrDisabled)
	}
	if o.backend == nil {
		return errBlobBackendUnavailable
	}
	return nil
}

// openBlobOps 按 platform.blobs.* 装配一个「必须可用」的后端。
//
// 与 server 装配的分界只有一句话的差别：server 是「构造错误拒启、就绪错误降级」，这里是
// 「构造错误与就绪错误都直接退出」。就绪失败额外补一句提示，免得运维以为整个平台也挂了
// ——事实上 server 此刻多半还在正常服务基础业务。
func openBlobOps(ctx context.Context, cmd *cobra.Command, deps Dependencies) (*blobOps, error) {
	if deps.LoadConfig == nil {
		return nil, errors.New("config loader not wired")
	}
	// 平台剖面加载：blobs 要吃 platform.blobs.* 全键与 platform.store.*，CLI 剖面只有
	// driver/dir 两键（config/keys.go 的 cliPlatformKeys）。
	cfg, err := deps.LoadConfig(loadOptionsFromFlags(cmd, false))
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	blobs := cfg.Platform.Blobs
	maxBytes, err := blobs.MaxObjectSizeBytes()
	if err != nil {
		return nil, fmt.Errorf("platform.blobs.max_object_size: %w", err)
	}
	prefix, err := objectstore.NormalizePrefix(blobs.Prefix)
	if err != nil {
		return nil, fmt.Errorf("platform.blobs.prefix: %w", err)
	}
	st, _, err := openPlatformStore(cfg)
	if err != nil {
		return nil, err
	}
	// 登记表必须先存在：blobs 是运维命令，不替 k8s-ai db migrate 改 schema。把「库里还
	// 没有 objects 表」渲染成一句可执行的指引，比让驱动报 no such table 有用得多。
	if version, expected, err := st.MigrationStatus(ctx); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("read platform schema version: %w", err)
	} else if version < expected {
		_ = st.Close()
		return nil, fmt.Errorf("platform schema is at version %d, this build needs %d: run k8s-ai db migrate first",
			version, expected)
	}
	ops := &blobOps{cfg: cfg, st: st, driver: blobs.EffectiveDriver(), prefix: prefix}
	ops.maxBytes = maxBytes
	if ops.driver == config.BlobDriverOff {
		ops.disabled = true
		return ops, nil
	}

	backend, err := newBlobBackend(ctx, blobs, ops.driver, maxBytes, st)
	if err != nil {
		ops.Close()
		if errors.Is(err, errBlobBackendUnavailable) {
			return nil, err
		}
		return nil, fmt.Errorf("object storage backend: %w", err)
	}
	ops.backend = backend
	gc, err := service.NewBlobGC(backend, st.Objects())
	if err != nil {
		ops.Close()
		return nil, err
	}
	ops.gc = gc
	return ops, nil
}

// newBlobBackend 按 platform.blobs.* 构造一个后端，driver 由调用方指定。
//
// 它是从 openBlobOps 里抽出来的：配置里只有一个 platform.blobs.driver，而 `blobs migrate`
// 必须同时握着头尾两个后端（方案 15 §10.1）。两边共用这一个构造函数，是为了让「运维命令看到
// 的后端」和「server 装配出来的后端」除 driver 一个字段以外完全一致——迁移工具算错目标端的
// endpoint、prefix 或 bucket，后果比迁不动严重得多。
//
// 就绪失败包成 errBlobBackendUnavailable 上抛：运维命令没有降级可言，但文案要说清「只有
// 这条命令动不了，server 那边基础业务照跑」。构造期错误（形状非法）不包装，原样透出。
func newBlobBackend(ctx context.Context, blobs config.PlatformBlobsConfig, driver string,
	maxBytes int64, st store.Store) (objectstore.Store, error) {
	backend, err := objectstore.New(objectstore.Config{
		Driver:       driver,
		Prefix:       blobs.Prefix,
		Dir:          blobs.Dir,
		TmpDir:       blobs.TmpDir,
		MaxBytes:     maxBytes,
		Endpoint:     blobs.Endpoint,
		Region:       blobs.Region,
		UseTLS:       blobs.UseTLS,
		PathStyle:    blobs.PathStyle,
		Bucket:       blobs.Bucket,
		AccessKey:    blobs.AccessKey,
		SecretKey:    blobs.SecretKey,
		CreateBucket: blobs.CreateBucket,
		PresignTTL:   blobs.PresignTTL,
	}, func() (objectstore.Store, error) {
		return st.ObjectContent(blobs.Prefix, maxBytes)
	})
	if err != nil {
		return nil, err
	}
	if m, ok := backend.(objectstore.Maintainer); ok {
		if err := m.Ensure(ctx); err != nil {
			_ = backend.Close()
			return nil, fmt.Errorf("%w: driver=%s: %v (the server keeps serving chat/approval/audit without it; this command cannot)",
				errBlobBackendUnavailable, driver, err)
		}
	}
	return backend, nil
}

// newBlobsStatCmd 看对象：不带参数报总体存量，带 key 报单行元数据并与后端实存比对。
func newBlobsStatCmd(deps Dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "stat [blob-key]",
		Short: "Show object inventory, or one object with its backend state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ops, err := openBlobOps(cmd.Context(), cmd, deps)
			if err != nil {
				return err
			}
			defer ops.Close()
			w := cmd.OutOrStdout()

			if len(args) == 0 {
				stats, err := ops.st.Objects().ObjectStats(cmd.Context())
				if err != nil {
					return err
				}
				fmt.Fprintf(w, "driver=%s\n", ops.driver)
				if ops.disabled {
					fmt.Fprintln(w, "platform.blobs.driver=off: no content backend is wired")
				}
				var total, bytes int64
				for _, s := range stats {
					mark := ""
					if s.Driver != ops.driver {
						mark = fmt.Sprintf("  <- content lives on %q, run: k8s-ai blobs migrate --to %s", s.Driver, ops.driver)
					}
					total += s.Objects
					bytes += s.Bytes
					fmt.Fprintf(w, "  driver=%s objects=%d bytes=%d%s\n", s.Driver, s.Objects, s.Bytes, mark)
				}
				fmt.Fprintf(w, "total objects=%d bytes=%d (%s)\n", total, bytes, store.FormatBytes(bytes))
				return nil
			}

			meta, err := ops.st.Objects().Get(cmd.Context(), args[0])
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no objects row for %s", args[0])
			} else if err != nil {
				return err
			}
			fmt.Fprintf(w, "key=%s\nsha256=%s\nsize=%d\nmedia_type=%s\ncategory=%s\ndriver=%s\n"+
				"ref_count=%d\nstate=%s\ncreated_by=%s\ncreated_at=%s\nupdated_at=%s\n",
				meta.BlobKey, meta.SHA256, meta.Size, meta.MediaType, meta.Category, meta.Driver,
				meta.RefCount, meta.State, meta.CreatedBy,
				meta.CreatedAt.UTC().Format(time.RFC3339), meta.UpdatedAt.UTC().Format(time.RFC3339))
			if meta.DeletedAt != nil {
				fmt.Fprintf(w, "deleted_at=%s\n", meta.DeletedAt.UTC().Format(time.RFC3339))
			}
			if meta.Driver != ops.driver {
				fmt.Fprintf(w, "backend=other  content is on driver %q while this deployment runs %q\n", meta.Driver, ops.driver)
				fmt.Fprintf(w, "  not a loss: run k8s-ai blobs migrate --to %s to bring it over\n", ops.driver)
				return nil
			}
			if err := ops.requireBackend(); err != nil {
				fmt.Fprintf(w, "backend=unknown  %v\n", err)
				return nil
			}
			ref, err := ops.backend.Stat(cmd.Context(), meta.BlobKey)
			switch {
			case err == nil:
				state := "present"
				if ref.Size != meta.Size {
					state = fmt.Sprintf("SIZE MISMATCH (registry %d, backend %d)", meta.Size, ref.Size)
				}
				fmt.Fprintf(w, "backend=%s  %s size=%d\n", ops.driver, state, ref.Size)
			case errors.Is(err, objectstore.ErrNotFound):
				fmt.Fprintf(w, "backend=%s  MISSING  the row is alive but the content is gone (check backups, then re-run the scan)\n", ops.driver)
			default:
				fmt.Fprintf(w, "backend=%s  UNKNOWN  %v\n", ops.driver, err)
			}
			return nil
		},
	}
}

// newBlobsGetCmd 下载一个对象。driver 不符时**拒绝而不是报「不存在」**：把「换了后端没迁库」
// 渲染成「对象丢了」是本方案最贵的一类误判（§8.3、§10.1）。
func newBlobsGetCmd(deps Dependencies) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "get <blob-key>",
		Short: "Download one object by key (stdout by default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ops, err := openBlobOps(cmd.Context(), cmd, deps)
			if err != nil {
				return err
			}
			defer ops.Close()
			if err := ops.requireBackend(); err != nil {
				return err
			}
			key := args[0]
			meta, err := ops.st.Objects().Get(cmd.Context(), key)
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no objects row for %s", key)
			} else if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if meta.Driver != ops.driver {
				return fmt.Errorf("content of %s lives on driver %q but this deployment runs %q: run k8s-ai blobs migrate --to %s first (方案 15 §10.1)",
					key, meta.Driver, ops.driver, ops.driver)
			}
			if meta.State == store.ObjectStateDeleting {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s is being garbage collected (state=deleting)\n", key)
			}

			rc, ref, err := ops.backend.Get(cmd.Context(), key)
			if err != nil {
				return fmt.Errorf("read object %s: %w", key, err)
			}
			defer func() { _ = rc.Close() }()

			if output == "" || output == "-" {
				n, err := io.Copy(w, rc)
				if err != nil {
					return fmt.Errorf("write object to stdout: %w", err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "read %d bytes from %s (sha256=%s)\n", n, key, ref.SHA256)
				return nil
			}
			f, err := os.Create(output)
			if err != nil {
				return fmt.Errorf("create %s: %w", output, err)
			}
			defer func() { _ = f.Close() }()
			n, err := io.Copy(f, rc)
			if err != nil {
				return fmt.Errorf("write %s: %w", output, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("close %s: %w", output, err)
			}
			fmt.Fprintf(w, "wrote %d bytes to %s (sha256=%s)\n", n, output, ref.SHA256)
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "-", "destination file, or - for stdout")
	return cmd
}

// newBlobsPutCmd 手工写入一个对象并登记元数据行（回填/排障用，不是上传通道）。
//
// 登记这一步不能省：桶里有内容而表里没有行，下一轮差集就会把它当孤儿删掉。
//
// 终态必须是 orphaned + deleted_at=now：这一行没有任何业务行引用它，ready 会让它变成 GC
// 永远看不见的不死行。走法也只能是「Acquire 挂上引用、同事务里 Release 放掉」这条真实
// 引用通道：直接 Register 一个 orphaned 行在 db 后端根本不成立——内容 Put 会抢先把行建成
// ready，而 Register 的冲突分支刻意不动 state（幂等重放不许重置别人挂着的引用），结果就是
// 一行 ref_count=0、却永远不进 GC 视野的死人行，命令还告诉运维「宽限期后会被回收」。
// 走 Acquire/Release 之后两个后端行为一致，顺带白拿一条守卫：state=deleting 的行会被
// Acquire 拒掉，回填不会去踩正在回收的对象。
func newBlobsPutCmd(deps Dependencies) *cobra.Command {
	var category string
	var scope []string
	var ext string
	cmd := &cobra.Command{
		Use:   "put <file>",
		Short: "Upload a local file as an object and register it (ops/backfill only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ops, err := openBlobOps(cmd.Context(), cmd, deps)
			if err != nil {
				return err
			}
			defer ops.Close()
			if err := ops.requireBackend(); err != nil {
				return err
			}
			if !objectstore.ValidCategory(category) {
				return fmt.Errorf("--category must be one of %v, got %q", objectstore.Categories(), category)
			}
			if ext == "" {
				ext = filepath.Ext(args[0])
			}

			f, err := os.Open(args[0])
			if err != nil {
				return fmt.Errorf("open %s: %w", args[0], err)
			}
			defer func() { _ = f.Close() }()

			ref, err := ops.backend.Put(cmd.Context(), objectstore.Spec{
				Category:  category,
				Scope:     scope,
				Ext:       ext,
				CreatedBy: objectstore.SystemActor,
			}, f)
			if err != nil {
				return fmt.Errorf("put object: %w", err)
			}
			grace := ops.cfg.Platform.Blobs.GCGracePeriod
			if err := ops.st.WithTx(cmd.Context(), func(tx *store.Tx) error {
				if err := tx.Acquire(cmd.Context(), ref, ops.driver, objectstore.SystemActor); err != nil {
					return err
				}
				return tx.Release(cmd.Context(), ref.Key)
			}); err != nil {
				return fmt.Errorf("register object %s: %w", ref.Key, err)
			}
			row, err := ops.st.Objects().Get(cmd.Context(), ref.Key)
			if err != nil {
				return fmt.Errorf("read back object %s: %w", ref.Key, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "put ok: key=%s sha256=%s size=%d driver=%s state=%s ref_count=%d\n",
				ref.Key, ref.SHA256, ref.Size, ops.driver, row.State, row.RefCount)
			fmt.Fprintf(cmd.OutOrStdout(), "  it stays collectable: acquire it within %s or k8s-ai blobs gc will reclaim it\n", grace)
			return nil
		},
	}
	cmd.Flags().StringVar(&category, "category", objectstore.CategoryReports, fmt.Sprintf("object category (%v)", objectstore.Categories()))
	cmd.Flags().StringSliceVar(&scope, "scope", []string{"manual"}, "key scope segments (ids only, never file names)")
	cmd.Flags().StringVar(&ext, "ext", "", "key extension (default: taken from the file name)")
	return cmd
}

// newBlobsGCCmd 回收引用归零的对象，并用桶扫描差集兜住「行没了、内容还在」的残留。
func newBlobsGCCmd(deps Dependencies) *cobra.Command {
	var olderThan time.Duration
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Collect unreferenced objects past the grace period",
		Long: "Reclaim objects whose references dropped to zero more than gc_grace_period ago,\n" +
			"then sweep the backend for keys the registry has no row for.\n" +
			"\n" +
			"Order matters: rows are forgotten before content is deleted, so a crash in between\n" +
			"leaves a collectable orphan rather than a dangling reference. Objects whose row lives\n" +
			"on another driver are never touched (a migration in flight is not an orphan), and\n" +
			"keys outside platform.blobs.prefix are reported only, never deleted.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ops, err := openBlobOps(cmd.Context(), cmd, deps)
			if err != nil {
				return err
			}
			defer ops.Close()
			w := cmd.OutOrStdout()
			if ops.disabled {
				fmt.Fprintln(w, "platform.blobs.driver=off: nothing to collect")
				return nil
			}
			grace := olderThan
			if grace <= 0 {
				grace = ops.cfg.Platform.Blobs.GCGracePeriod
			}

			res, err := ops.gc.Run(cmd.Context(), service.GCOptions{Grace: grace, Prefix: ops.prefix, DryRun: dryRun})
			if err != nil {
				return err
			}
			mode := "collect"
			if dryRun {
				mode = "dry-run"
			}
			fmt.Fprintf(w, "blobs gc driver=%s mode=%s grace=%s cutoff=%s\n",
				res.Driver, mode, grace, res.Cutoff.UTC().Format(time.RFC3339))
			fmt.Fprintf(w, "  referenced: candidates=%d claimed=%d rows-removed=%d content-deleted=%d revived=%d skipped-foreign=%d reclaimed=%s\n",
				res.Candidates, res.Claimed, res.RowsRemoved, res.ContentDeleted, res.Revived,
				res.SkippedForeign, store.FormatBytes(res.ReclaimedBytes))
			if res.SkippedForeign > 0 {
				fmt.Fprintf(w, "  unmanaged:  %d object(s) live on another driver and were left alone - run: k8s-ai blobs migrate --to %s\n",
					res.SkippedForeign, res.Driver)
			}
			fmt.Fprintf(w, "  bucket:     listed=%d orphans=%d deleted=%d bytes=%s\n",
				res.BucketKeys, res.Orphans, res.OrphansDel, store.FormatBytes(res.OrphanBytes))
			if res.Foreign > 0 {
				fmt.Fprintf(w, "  foreign:    reported-only=%d (other-root=%d invalid-key=%d) - left alone, this bucket may be shared\n",
					res.Foreign, res.OtherRoot, res.RejectedKey)
			}
			if res.Empty() {
				fmt.Fprintln(w, "nothing to collect")
			} else if dryRun {
				fmt.Fprintln(w, "dry-run: no row and no object was touched")
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&olderThan, "older-than", 0, "override platform.blobs.gc_grace_period (0 keeps the configured value)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be collected, change nothing")
	return cmd
}

// newBlobsVerifyCmd 做一致性对账（方案 15 §12.2）：默认逐个 Stat 比 size，--deep 下载重算
// sha256，--strict 把「任一不一致」变成非零退出，供恢复流程当准出门禁。
func newBlobsVerifyCmd(deps Dependencies) *cobra.Command {
	var category string
	var deep bool
	var strict bool
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Reconcile the object registry against the content backend",
		Long: "Checks the invariant behind every download: each ready objects row has content in the\n" +
			"backend with the same key, size and sha256.\n" +
			"\n" +
			"Default mode only stats each object, so a full pass is cheap enough to run any time.\n" +
			"--deep downloads and recomputes sha256 (silent corruption has the right size); ETags are\n" +
			"never a substitute because S3 stores an MD5, not our sha256.\n" +
			"\n" +
			"A backend that cannot be reached aborts the run instead of marking objects missing -\n" +
			"one outage must not sentence healthy data to state=missing. Rows whose content lives on\n" +
			"another driver are reported as pending migration, and rows mid garbage collection are\n" +
			"skipped; neither counts as inconsistent. Rows are only rewritten after the whole\n" +
			"forward pass succeeds.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ops, err := openBlobOps(cmd.Context(), cmd, deps)
			if err != nil {
				return err
			}
			defer ops.Close()
			w := cmd.OutOrStdout()
			if ops.disabled {
				return verifyWithoutBackend(cmd, ops)
			}

			verifier, err := service.NewBlobVerifier(ops.backend, ops.st.Objects())
			if err != nil {
				return err
			}
			res, err := verifier.Run(cmd.Context(), service.VerifyOptions{
				Category: category,
				Prefix:   ops.prefix,
				Deep:     deep,
			})
			if err != nil {
				// 中止与不一致是两件事：这句文案必须说清「这轮没结论」，否则运维会以为
				// 桶里真的少了东西。
				return fmt.Errorf("verify aborted (no verdict was reached): %w", err)
			}
			mode := "stat"
			if deep {
				mode = "deep"
			}
			scope := "all"
			if category != "" {
				scope = category
			}
			fmt.Fprintf(w, "blobs verify driver=%s mode=%s category=%s prefix=%s\n",
				res.Driver, mode, scope, ops.prefix)
			fmt.Fprintf(w, "  rows:      checked=%d ok=%d size-mismatch=%d missing=%d checksum-mismatch=%d address-mismatch=%d deep-verified=%d\n",
				res.Rows, res.OK, res.SizeMismatch, res.Missing, res.ChecksumMismatch, res.AddressMismatch, res.DeepChecked)
			printVerifySamples(w, "  sample", res)
			if res.RowsMarkedMissing > 0 {
				fmt.Fprintf(w, "  marked:    %d row(s) -> state=missing (restore the content, then re-put to heal)\n", res.RowsMarkedMissing)
			}
			if res.Deleting > 0 {
				fmt.Fprintf(w, "  garbage collection in flight: %d row(s) skipped (state=deleting)\n", res.Deleting)
			}
			if res.ForeignDriver > 0 {
				fmt.Fprintf(w, "  pending:   %d row(s) still on another driver - not a loss, run: k8s-ai blobs migrate --to %s\n",
					res.ForeignDriver, res.Driver)
			}
			fmt.Fprintf(w, "  bucket:    listed=%d orphans=%d bytes=%s foreign=%d (reverse scan ignores driver)\n",
				res.BucketKeys, res.Orphans, store.FormatBytes(res.OrphanBytes), res.Foreign)
			if bad := res.Inconsistent(); bad > 0 {
				fmt.Fprintf(w, "INCONSISTENT: %d difference(s); registry rows are not trustworthy without the content\n", bad)
				if strict {
					return fmt.Errorf("verify found %d inconsistency(-ies) (--strict)", bad)
				}
				return nil
			}
			fmt.Fprintln(w, "consistent: every checked row has matching content")
			return nil
		},
	}
	cmd.Flags().StringVar(&category, "category", "", "only check one category (default: all)")
	cmd.Flags().BoolVar(&deep, "deep", false, "download each object and recompute sha256 (full read traffic)")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero on any inconsistency (restore gate)")
	return cmd
}

// verifyWithoutBackend 回答 driver=off 下的对账：没有内容后端可对，但登记表还是活的，
// 一句「没开」加一句「表里还有 N 行在等它们的后端」，比假装扫干净了诚实得多。
func verifyWithoutBackend(cmd *cobra.Command, ops *blobOps) error {
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, "platform.blobs.driver=off: no content backend to verify against")
	stats, err := ops.st.Objects().ObjectStats(cmd.Context())
	if err != nil {
		return err
	}
	var rows int64
	var byDriver []string
	for _, s := range stats {
		rows += s.Objects
		byDriver = append(byDriver, fmt.Sprintf("%s=%d", s.Driver, s.Objects))
	}
	if rows > 0 {
		fmt.Fprintf(w, "  registry still holds %d object(s) [%s]: point platform.blobs.driver back at the backend that has them\n",
			rows, strings.Join(byDriver, " "))
	}
	return nil
}

// printVerifySamples 打问题样本：规模看计数，下一步动作看 key。
func printVerifySamples(w io.Writer, label string, res service.VerifyResult) {
	for _, group := range []struct {
		name string
		keys []string
	}{
		{"size", res.SizeMismatchKeys},
		{"missing", res.MissingKeys},
		{"checksum", res.ChecksumMismatchKeys},
		{"address", res.AddressMismatchKeys},
	} {
		if len(group.keys) > 0 {
			fmt.Fprintf(w, "%s %-8s %s\n", label, group.name+":", strings.Join(group.keys, ", "))
		}
	}
}

// newBlobsMigrateCmd 把对象从当前配置的后端搬到另一个后端（方案 15 §10.1）。
//
// 它是「切换 platform.blobs.driver」唯一的前置动作：server 只按配置装配一个后端，改完配置
// 之后 objects.driver 还写着旧后端的行是读不到的（下载端点回 409，而不是假装对象不存在）。
// 所以这条命令必须在**改配置之前**跑，文案与退出码都按这个前提写。
func newBlobsMigrateCmd(deps Dependencies) *cobra.Command {
	var to string
	var category string
	var dryRun bool
	var pruneSource bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Move objects onto another backend before switching platform.blobs.driver",
		Long: "Move objects from the backend named by platform.blobs.driver to the one you are about to\n" +
			"switch to, then flip platform.blobs.driver once nothing is left behind.\n" +
			"\n" +
			"Run this BEFORE changing the configuration. The server wires exactly one content backend,\n" +
			"so a row whose driver still names the old one is not readable after the config moves - the\n" +
			"download endpoint answers 409 (pending migration) rather than pretending the object is gone.\n" +
			"\n" +
			"Per object the order is fixed: write the content to the target, verify its sha256, flip\n" +
			"objects.driver in its own transaction, and only then delete the source copy (--prune-source,\n" +
			"off by default). An interrupted run therefore leaves both copies whole and converges when\n" +
			"re-run; --dry-run prints the same numbers and changes nothing.\n" +
			"\n" +
			"Objects whose row names a third driver are left alone, and the target is never overwritten:\n" +
			"a size difference under an existing key is reported as a failure, not patched over.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ops, err := openBlobOps(ctx, cmd, deps)
			if err != nil {
				return err
			}
			defer ops.Close()
			w := cmd.OutOrStdout()

			target := strings.ToLower(strings.TrimSpace(to))
			if !objectstore.ValidDriver(target) {
				return fmt.Errorf("--to must be one of db|dir|s3, got %q (off is not a place to store content)", to)
			}
			if ops.disabled {
				return fmt.Errorf("%w: platform.blobs.driver=off, so there is no source backend to migrate from "+
					"(the source is always the configured driver; 方案 15 §10.1)", errBlobBackendUnavailable)
			}
			if target == ops.driver {
				fmt.Fprintf(w, "platform.blobs.driver is already %q: nothing to migrate\n", target)
				fmt.Fprintf(w, "  if objects still report another driver, run: k8s-ai blobs verify\n")
				return nil
			}

			// 同一份配置换个 driver 再构造一次：除 driver 之外 endpoint/bucket/prefix/dir
			// 完全同源，迁移工具算错目标端比迁不动更糟。
			dst, err := newBlobBackend(ctx, ops.cfg.Platform.Blobs, target, ops.maxBytes, ops.st)
			if err != nil {
				return fmt.Errorf("target backend: %w", err)
			}
			defer func() { _ = dst.Close() }()

			migrator, err := service.NewBlobMigrator(ops.backend, dst, ops.st)
			if err != nil {
				return err
			}
			res, err := migrator.Run(ctx, service.BlobMigrateOptions{
				Category:    category,
				MaxBytes:    store.ObjectContentLimit(ops.maxBytes),
				DryRun:      dryRun,
				PruneSource: pruneSource,
			})
			if err != nil {
				// 中止不等于失败：这句要说清「已翻的行是好的，环境修好重跑即可」。
				return fmt.Errorf("migrate aborted after %d flip(s): %w", res.Flipped, err)
			}
			printMigrateResult(cmd, ops, res, target, category, dryRun, pruneSource)
			if res.Failed > 0 {
				return fmt.Errorf("migrate failed for %d object(s); their source copies were left untouched", res.Failed)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "target driver to migrate onto (db|dir|s3)")
	cmd.Flags().StringVar(&category, "category", "", "only move one category (default: all)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would move, change nothing")
	cmd.Flags().BoolVar(&pruneSource, "prune-source", false, "delete the source copy of each flipped object")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

// printMigrateResult 按「内容 / 元数据 / 源端」三段打账目，最后回答运维真正想知道的那句：
// 现在能不能改配置。
func printMigrateResult(cmd *cobra.Command, ops *blobOps, res service.BlobMigrateResult,
	target, category string, dryRun, pruneSource bool) {
	w := cmd.OutOrStdout()
	mode := "copy"
	if dryRun {
		mode = "dry-run"
	}
	scope := "all"
	if category != "" {
		scope = category
	}
	fmt.Fprintf(w, "blobs migrate %s -> %s mode=%s category=%s prefix=%s\n",
		res.DriverFrom, res.DriverTo, mode, scope, ops.prefix)
	fmt.Fprintf(w, "  scanned:   rows=%d candidates=%d other-driver=%d deleting=%d\n",
		res.Scanned, res.Candidates, res.SkippedOther, res.Deleting)
	fmt.Fprintf(w, "  content:   copied=%d already-present=%d unreadable=%d\n",
		res.Copied, res.Present, res.Unreadable)
	fmt.Fprintf(w, "  metadata:  flipped=%d conflict=%d bytes=%s\n",
		res.Flipped, res.Conflict, store.FormatBytes(res.Bytes))
	if pruneSource {
		fmt.Fprintf(w, "  source:    pruned=%d prune-failed=%d\n", res.Pruned, res.PruneFailed)
	} else if !dryRun {
		fmt.Fprintf(w, "  source:    kept (reclaim with --prune-source once the switch is verified)\n")
	}
	for _, msg := range res.Failures {
		fmt.Fprintf(w, "  failed:    %s\n", msg)
	}

	remaining := migrateRemaining(cmd, ops, res.DriverFrom)
	if dryRun {
		fmt.Fprintf(w, "dry-run: no object and no row was touched\n")
	}
	if remaining > 0 {
		fmt.Fprintf(w, "  remaining: %d object(s) still on %q - finish the move before setting platform.blobs.driver=%s\n",
			remaining, res.DriverFrom, target)
	} else {
		fmt.Fprintf(w, "  remaining: none on %q - platform.blobs.driver=%s is now safe, run k8s-ai blobs verify to confirm\n",
			res.DriverFrom, target)
	}
	if res.Unreadable > 0 {
		fmt.Fprintf(w, "  note:      %d row(s) had no content to move on %q: those are broken rows, not misplaced ones (k8s-ai blobs verify)\n",
			res.Unreadable, res.DriverFrom)
	}
}

// migrateRemaining 报「源后端上还剩几行没接走」。它读的是 objects 表的分组统计而不是本次
// 计数的余数：迁移被中断、并发写入、别的 driver 都会让「本轮计数」不等于「库里存量」，而运维
// 换配置前要看的就是库里存量。
func migrateRemaining(cmd *cobra.Command, ops *blobOps, driver string) int64 {
	stats, err := ops.st.Objects().ObjectStats(cmd.Context())
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: cannot recount objects on %q: %v\n", driver, err)
		return 0
	}
	for _, s := range stats {
		if s.Driver == driver {
			return s.Objects
		}
	}
	return 0
}
