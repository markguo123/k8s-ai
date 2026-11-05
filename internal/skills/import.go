package skills

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Procedural Skill 导入大小与安全上限（v1 单/多篇 SKILL.md + 单个技能包 zip）。
const (
	// maxZipSizeBytes 限制技能包 zip 的解压后总大小（16MiB，防御 zip 炸弹）。
	maxZipSizeBytes = 16 << 20
	// maxZipFileCount 限制技能包文件条目数（含目录）。
	maxZipFileCount = 200
	// maxZipPathLen 限制单个解压路径长度。
	maxZipPathLen = 1024
	// maxZipDepth 限制解压目录深度。
	maxZipDepth = 32
)

// proceduralAssetExts 是技能包内可随包入库的资产类型白名单（P3.8.3）。
// scripts/ 一律按惰性资产处理，不在此白名单内；其余不在白名单的文件直接拒绝。
var proceduralAssetExts = map[string]bool{
	".md":   true,
	".yaml": true,
	".yml":  true,
	".json": true,
	".txt":  true,
	".csv":  true,
}

// ParseProceduralDocs 解析原始 SKILL.md 文本（P3.8.2），支持单篇或
// 用 --- 分隔的多篇。每篇要求 frontmatter 声明 name 与 description；
// frontmatter 之后的内容作为不可信正文 Body 入库，任何内容都不执行。
func ParseProceduralDocs(raw []byte) ([]Skill, error) {
	docs, err := splitProceduralDocs(string(raw))
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, errors.New("no procedural skill document found")
	}
	out := make([]Skill, 0, len(docs))
	seen := map[string]bool{}
	for i, doc := range docs {
		s, err := parseOneProceduralDoc(doc)
		if err != nil {
			return nil, fmt.Errorf("skill doc %d: %w", i+1, err)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("duplicate procedural skill name %q", s.Name)
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	return out, nil
}

// ParseProceduralZip 解析单个技能包 zip（P3.8.3）：安全解压、资产白名单与
// scripts/ 惰性化，并提取包内唯一 SKILL.md 的方法论正文。多技能 zip 明确延后。
func ParseProceduralZip(data []byte) ([]Skill, error) {
	assets, skillBody, err := collectZipAssets(data)
	if err != nil {
		return nil, err
	}
	s, err := parseOneProceduralDoc(skillBody)
	if err != nil {
		return nil, fmt.Errorf("skill package SKILL.md: %w", err)
	}
	if len(s.Body) > proceduralMaxBodyBytes {
		return nil, fmt.Errorf("skill package SKILL.md body exceeds %d bytes", proceduralMaxBodyBytes)
	}
	s.Assets = assets
	return []Skill{s}, nil
}

// splitProceduralDocs 把单/多篇 SKILL.md 文本切成独立文档块（保留原文）。
// 采用前置扫描：每篇必须以 --- 开头且随后有被 ---/... 闭合的 frontmatter；
// 后续出现的「--- 后跟合法 frontmatter」才是下一篇边界，正文里的 --- 分隔线
// （后面不是 frontmatter）按正文内容处理，避免误切多档。
func splitProceduralDocs(raw string) ([]string, error) {
	text := strings.ReplaceAll(strings.TrimPrefix(raw, "\ufeff"), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	// 去掉文件头与文档间的空行。
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) == 0 {
		return nil, nil
	}
	docs := make([]string, 0, 1)
	start := 0
	for start < len(lines) {
		sliceStart, fmOpen, ok := proceduralDocStart(lines, start)
		if !ok {
			return nil, fmt.Errorf("skill doc must start with frontmatter delimiter ---")
		}
		closeIdx, ok := frontmatterClose(lines, fmOpen)
		if !ok {
			return nil, fmt.Errorf("frontmatter not closed (near line %d)", fmOpen+1)
		}
		// 下一篇起点：frontmatter 结束后第一个「--- 后跟合法 frontmatter」的行。
		end := -1
		for j := closeIdx + 1; j < len(lines); j++ {
			if _, _, ok := proceduralDocStart(lines, j); ok {
				end = j
				break
			}
		}
		if end < 0 {
			end = len(lines)
		}
		docs = append(docs, strings.Join(lines[sliceStart:end], "\n"))
		start = end
		for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
			start++
		}
	}
	return docs, nil
}

// proceduralDocStart 判断 lines[i] 是否为一篇 procedural 技能的起点并返回
// 三个值：切片起点 sliceStart、frontmatter 开边界行 fmOpen、是否命中。允许
// 可选的 ```markdown / ```yaml 围栏开标签后紧跟 --- frontmatter 开边界（真实
// skill 包常见写法）；围栏行会被 parseOneProceduralDoc 自行移除。
func proceduralDocStart(lines []string, i int) (sliceStart, fmOpen int, ok bool) {
	if i < len(lines) && isFrontmatterOpen(lines, i) {
		return i, i, true
	}
	if i+1 < len(lines) && isFenceOpenLine(lines[i]) && isFrontmatterOpen(lines, i+1) {
		return i, i + 1, true
	}
	return 0, 0, false
}

// isFenceOpenLine 判断一行是否为尖角代码围栏开标签（可带语言标签）。
func isFenceOpenLine(line string) bool {
	if !strings.HasPrefix(line, "```") {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, "```"))
	// `` ``` (三个反引号) ``` `` 形式简单识别：标签后不再出现闭合反引号。
	return !strings.Contains(rest, "```")
}

func isFrontmatterOpen(lines []string, i int) bool {
	if i >= len(lines) || lines[i] != "---" {
		return false
	}
	for j := i + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "" {
			return false // frontmatter 内出现空行 → 不是干净的 SKILL.md 边界
		}
		// 围栏开行出现在结束界之前 → 当前 --- 其实是上一篇/分隔线，
		// 下一行的 --- 才真正开启 frontmatter（围栏包裹写法），此处不当作边界。
		if isFenceOpenLine(lines[j]) {
			return false
		}
		if lines[j] == "---" || lines[j] == "..." {
			return j > i+1 // 至少一行业面前导
		}
	}
	return false
}

func frontmatterClose(lines []string, i int) (int, bool) {
	for j := i + 1; j < len(lines); j++ {
		if lines[j] == "---" || lines[j] == "..." {
			return j, true
		}
	}
	return 0, false
}

// parseOneProceduralDoc 解析单篇 SKILL.md 文本为 procedural Skill。
// frontmatter 只提取 name/description/version，其余用户字段（license 等）忽略；
// 正文 Body 作为不可信文本，交由上层校验器做体积与内容约束。
func parseOneProceduralDoc(doc string) (Skill, error) {
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) > 0 {
		lines[0] = strings.TrimPrefix(lines[0], "\ufeff")
	}
	stripFence := false
	if len(lines) >= 2 && isFenceOpenLine(lines[0]) && isFrontmatterOpen(lines, 1) {
		stripFence = true
		lines = lines[1:]
	}
	if len(lines) == 0 || lines[0] != "---" {
		return Skill{}, errors.New("SKILL.md must start with frontmatter ---")
	}
	closeIdx := -1
	for j := 1; j < len(lines); j++ {
		if lines[j] == "---" || lines[j] == "..." {
			closeIdx = j
			break
		}
	}
	if closeIdx < 0 {
		return Skill{}, errors.New("frontmatter not closed")
	}
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Version     string `yaml:"version"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:closeIdx], "\n")), &meta); err != nil {
		return Skill{}, fmt.Errorf("parse frontmatter: %w", err)
	}
	version := meta.Version
	if version == "" {
		version = "1.0.0"
	}
	if strings.TrimSpace(meta.Name) == "" {
		return Skill{}, errors.New("SKILL.md must declare a name in frontmatter")
	}
	if strings.TrimSpace(meta.Description) == "" {
		return Skill{}, errors.New("SKILL.md must declare a description in frontmatter")
	}
	bodyStart := closeIdx + 1
	// 围栏包裹 frontmatter 时，跳过 frontmatter 结尾后紧邻的围栏闭行，正文不自带 ```。
	if stripFence && bodyStart < len(lines) && strings.TrimSpace(lines[bodyStart]) == "```" {
		bodyStart++
	}
	return Skill{
		Type:        TypeProcedural,
		Source:      SourceCustom,
		Name:        strings.TrimSpace(meta.Name),
		Description: strings.TrimSpace(meta.Description),
		Version:     version,
		Body:        strings.TrimSpace(strings.Join(lines[bodyStart:], "\n")),
	}, nil
}

// collectZipAssets 安全展开技能包 zip：校验路径穿越/符号链接/非普通文件、
// 深度/文件数/总大小上限、资产类型白名单，并提取包内唯一 SKILL.md 的正文。
func collectZipAssets(data []byte) ([]SkillAsset, string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", fmt.Errorf("open skill package: %w", err)
	}
	if len(zr.File) > maxZipFileCount {
		return nil, "", fmt.Errorf("skill package file count %d exceeds %d", len(zr.File), maxZipFileCount)
	}
	type zipEntry struct {
		file    *zip.File
		norm    string
		content []byte
	}
	var entries []zipEntry
	topDirs := map[string]bool{}
	var total int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			return nil, "", fmt.Errorf("skill package contains symlink %q", f.Name)
		}
		if !mode.IsRegular() {
			return nil, "", fmt.Errorf("skill package contains non-regular file %q", f.Name)
		}
		norm, err := safeZipPath(f.Name)
		if err != nil {
			return nil, "", err
		}
		if norm == "" {
			continue
		}
		if f.UncompressedSize64 > maxZipSizeBytes || total+int64(f.UncompressedSize64) > maxZipSizeBytes {
			return nil, "", fmt.Errorf("skill package exceeds total size limit")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, "", fmt.Errorf("open %q: %w", norm, err)
		}
		content, readErr := io.ReadAll(io.LimitReader(rc, maxZipSizeBytes+1))
		_ = rc.Close()
		if readErr != nil {
			return nil, "", fmt.Errorf("read %q: %w", norm, readErr)
		}
		// 声明的尺寸与实际读取不一致时以实读为准强校验，防御 zip 头谎报大小。
		if int64(len(content)) > maxZipSizeBytes {
			return nil, "", fmt.Errorf("skill package file %q exceeds size limit", norm)
		}
		total += int64(len(content))
		if i := strings.IndexByte(norm, '/'); i > 0 {
			topDirs[norm[:i]] = true
		}
		entries = append(entries, zipEntry{file: f, norm: norm, content: content})
	}
	if len(entries) == 0 {
		return nil, "", errors.New("skill package must contain a SKILL.md")
	}
	// 根的 SKILL.md 作为主文；若包内无根 SKILL.md，则容忍“整体被单层目录包裹”的常见
	// 压缩形态（zip 技能文件夹本身），剥掉该统一顶层目录后仍须唯一命中根 SKILL.md。
	stripPrefix := ""
	hasRootSkill := false
	for _, e := range entries {
		if strings.EqualFold(e.norm, "SKILL.md") {
			hasRootSkill = true
			break
		}
	}
	if !hasRootSkill {
		if len(topDirs) != 1 {
			return nil, "", errors.New("skill package must contain exactly one SKILL.md (multi-skill zip not supported)")
		}
		for d := range topDirs {
			stripPrefix = d
		}
	}
	var assets []SkillAsset
	skillCount := 0
	skillBody := ""
	for _, e := range entries {
		norm := e.norm
		if stripPrefix != "" {
			norm = strings.TrimPrefix(norm, stripPrefix+"/")
		}
		base := filepath.Base(norm)
		inert := isScriptPath(norm)
		// 只有根目录 SKILL.md 是技能主文；scripts/SKILL.md 上面已判 inert=true，
		// 落到下面的资产清单当成惰性脚本资产，绝不作为主文。
		if strings.EqualFold(norm, "SKILL.md") {
			if skillCount >= 1 {
				return nil, "", errors.New("skill package must contain exactly one root SKILL.md")
			}
			skillCount++
			skillBody = string(e.content)
			continue
		}
		// 非根目录且不在 scripts/ 下的 SKILL.md 会造成主文歧义，整包拒绝
		// （多 Skill 打包明确延后到 P3.8 之后）。
		if strings.EqualFold(base, "SKILL.md") && !inert {
			return nil, "", fmt.Errorf("skill package contains ambiguous SKILL.md %q", norm)
		}
		ext := strings.ToLower(path.Ext(norm))
		if !inert && !proceduralAssetExts[ext] {
			return nil, "", fmt.Errorf("skill package asset %q has unsupported type %q (allowed: md/yaml/json/txt/csv)", norm, ext)
		}
		assets = append(assets, SkillAsset{
			Name:   base,
			Path:   norm,
			Kind:   assetKind(inert),
			Size:   int64(len(e.content)),
			SHA256: fmt.Sprintf("%x", sha256.Sum256(e.content)),
			Inert:  inert,
		})
	}
	if skillCount == 0 {
		return nil, "", errors.New("skill package must contain a SKILL.md")
	}
	return assets, skillBody, nil
}

// safeZipPath 规范化并校验 zip 条目路径：拒绝绝对路径、目录穿越、Windows
// 盘符前缀、超长路径与超深目录，返回以 / 分隔的相对路径；空路径返回 ""。
func safeZipPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if len(p) > maxZipPathLen {
		return "", fmt.Errorf("skill package path too long: %q", p)
	}
	norm := strings.ReplaceAll(p, "\\", "/")
	if path.IsAbs(norm) || strings.HasPrefix(norm, "~") {
		return "", fmt.Errorf("skill package path must be relative: %q", p)
	}
	clean := path.Clean(norm)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("skill package path escapes archive root: %q", p)
	}
	// Windows 盘符前缀（C:/...）在跨平台解压时也属于越界，直接拒绝。
	if len(clean) >= 2 && clean[1] == ':' {
		return "", fmt.Errorf("skill package path must be relative: %q", p)
	}
	depth := 0
	for _, seg := range strings.Split(clean, "/") {
		if seg != "" && seg != "." {
			depth++
		}
	}
	if depth > maxZipDepth {
		return "", fmt.Errorf("skill package path exceeds depth limit: %q", p)
	}
	return clean, nil
}

// isScriptPath 判断资产是否位于 scripts/ 目录：这类资产只入库元数据并标记
// inert=true，永不进入 Agent 工具路径，任何内容都不执行。
func isScriptPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "scripts" {
			return true
		}
	}
	return false
}

func assetKind(inert bool) string {
	if inert {
		return "script"
	}
	return "text"
}
