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
	cmd.AddCommand(newBlobsStatCmd(deps), newBlobsGetCmd(deps), newBlobsPutCmd(deps), newBlobsGCCmd(deps))
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
	if ops.driver == config.BlobDriverOff {
		ops.disabled = true
		return ops, nil
	}

	backend, err := objectstore.New(objectstore.Config{
		Driver:       ops.driver,
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
		ops.Close()
		return nil, fmt.Errorf("object storage backend: %w", err)
	}
	if m, ok := backend.(objectstore.Maintainer); ok {
		if err := m.Ensure(ctx); err != nil {
			_ = backend.Close()
			_ = st.Close()
			return nil, fmt.Errorf("%w: %v (the server keeps serving chat/approval/audit without it; this command cannot)",
				errBlobBackendUnavailable, err)
		}
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
// 登记成 state=orphaned + deleted_at=now（而不是 ready）是刻意选的终态：这一行没有任何
// 业务行引用它，Ready 会让它变成 GC 永远看不见的不死行。orphaned 正对应「先上传、等
// Acquire 来认领」这条既有路径——认领就复活成 ready，宽限期内没人认领就按正常生命周期
// 被收走，运维回填失败不会在库里留下永久的垃圾。
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
			now := time.Now()
			grace := ops.cfg.Platform.Blobs.GCGracePeriod
			if err := ops.st.Objects().Register(cmd.Context(), &store.ObjectMeta{
				BlobKey:   ref.Key,
				SHA256:    ref.SHA256,
				Size:      ref.Size,
				MediaType: ref.MediaType,
				Category:  category,
				Driver:    ops.driver,
				State:     store.ObjectStateOrphaned,
				DeletedAt: &now,
				CreatedBy: objectstore.SystemActor,
			}); err != nil {
				return fmt.Errorf("register object %s: %w", ref.Key, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "put ok: key=%s sha256=%s size=%d driver=%s state=orphaned ref_count=0\n",
				ref.Key, ref.SHA256, ref.Size, ops.driver)
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
