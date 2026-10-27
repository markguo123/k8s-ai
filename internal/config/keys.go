package config

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ValidateMode 选择 `config validate` 的校验剖面（P6.12 ⑧）：server 只关心
// platform: 静态段，cli 关心全量引擎键。两者都会校验已加载的配置并告警未识别键。
type ValidateMode string

const (
	// ValidateModeServer 校验平台静态配置文件（默认 config.yaml）。
	ValidateModeServer ValidateMode = "server"
	// ValidateModeCLI 校验终端直用配置（默认 cli.yaml，缺失回退 config.yaml）。
	ValidateModeCLI ValidateMode = "cli"
)

// ParseValidateMode 解析 --mode 取值。
func ParseValidateMode(s string) (ValidateMode, error) {
	switch ValidateMode(strings.TrimSpace(strings.ToLower(s))) {
	case "", ValidateModeServer:
		return ValidateModeServer, nil
	case ValidateModeCLI:
		return ValidateModeCLI, nil
	default:
		return "", fmt.Errorf("invalid --mode %q (server|cli)", s)
	}
}

// unknownKeyPrefix 是未识别键告警的固定前缀，测试据此取出键名。
const unknownKeyPrefix = "未识别的配置键 "

// engineSections 是终端直用（chat/scan）消费的引擎键顶层段。平台进程不消费它们：
// 对应的业务配置唯一权威源是 DB settings 表（ADR-031）。
var engineSections = []string{"kubernetes", "llm", "scan", "report", "agent", "rules"}

// staticEngineSections 是 engineSections 里允许出现在平台静态配置文件中的段。
// 这两个段只承载「这台实例的文件系统形状」——容器里根文件系统只读，报告与会话/审计
// 必须显式指向可写卷（deploy/base/configmap.yaml 就是这种写法），不属于动态业务配置。
var staticEngineSections = map[string]bool{"report": true, "agent": true}

// platformStaticEngineKeys 是平台静态配置文件（server 剖面）允许的引擎侧键白名单。
// 白名单之外的引擎键出现在平台文件里，就会与配置中心形成双配置源，因此单独告警。
var platformStaticEngineKeys = map[string]bool{
	"report.directory":  true,
	"report.format":     true,
	"agent.session_dir": true,
	"agent.audit_dir":   true,
}

// keyIndex 是 Config 可识别键的索引，分三类：
//   - leaf：标量与 map 字段的可写键（KnownKeys 返回这些）；
//   - node：结构体父段（llm、platform.server），本身不是配置项但必须识别，
//     否则未识别键告警会停在段名上，报不出真正写错的那一行；
//   - prefix：map 字段（如 llm.thinking）的子键前缀，子键由使用方给出。
type keyIndex struct {
	leaf   map[string]bool
	node   map[string]bool
	prefix map[string]bool
}

func (idx keyIndex) known(key string) bool {
	if idx.leaf[key] || idx.node[key] {
		return true
	}
	for p := range idx.prefix {
		if strings.HasPrefix(key, p+".") {
			return true
		}
	}
	return false
}

// KnownKeys 返回 Config 结构体可识别的全部叶子配置键（点分路径，已排序）。
// map 类型字段只记自身，不含其任意子键。
func KnownKeys() []string {
	idx := buildKeyIndex()
	keys := make([]string, 0, len(idx.leaf))
	for k := range idx.leaf {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func buildKeyIndex() keyIndex {
	idx := keyIndex{leaf: map[string]bool{}, node: map[string]bool{}, prefix: map[string]bool{}}
	indexStructKeys(reflect.TypeOf(Config{}), "", &idx)
	return idx
}

func indexStructKeys(t reflect.Type, prefix string, idx *keyIndex) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // 非导出字段不参与配置解析
		}
		name := strings.TrimSpace(strings.Split(f.Tag.Get("mapstructure"), ",")[0])
		if name == "" || name == "-" {
			continue
		}
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch ft.Kind() {
		case reflect.Struct:
			idx.node[key] = true
			indexStructKeys(ft, key, idx)
		case reflect.Map:
			idx.leaf[key] = true
			idx.prefix[key] = true
		default:
			idx.leaf[key] = true
		}
	}
}

// Diagnostics 检查配置文件内容与该校验剖面的匹配度，返回告警（不阻断校验）：
// 未识别键、剖面错放的段、YAML 明文 DSN。path 为空（无配置文件）时返回空。
func Diagnostics(path string, mode ValidateMode) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if len(doc) == 0 {
		return nil, nil
	}
	leaves := map[string]any{}
	flattenKeys(doc, "", leaves)

	idx := buildKeyIndex()
	reported := map[string]bool{}
	var unknown []string
	for key := range leaves {
		if idx.known(key) {
			continue
		}
		// 只报最浅的未知节点：未知段下的多个子键合并成一条告警。
		shallow := shallowestUnknown(key, idx)
		if shallow == "" || reported[shallow] {
			continue
		}
		reported[shallow] = true
		unknown = append(unknown, shallow)
	}
	sort.Strings(unknown)

	var warnings []string
	for _, key := range unknown {
		warnings = append(warnings, fmt.Sprintf("%s%s：已忽略，请检查拼写或版本是否不匹配", unknownKeyPrefix, key))
	}

	sections := make([]string, 0, len(doc))
	for key := range doc {
		sections = append(sections, key)
	}
	sort.Strings(sections)
	for _, section := range sections {
		switch mode {
		case ValidateModeServer:
			if staticEngineSections[section] {
				if offenders := misplacedStaticKeys(section, leaves); len(offenders) > 0 {
					warnings = append(warnings, fmt.Sprintf(
						"%s: 段里这些键不是平台静态落盘配置，平台侧请在 Web 配置中心维护：%s",
						section, strings.Join(offenders, "、")))
				}
				continue
			}
			if containsString(engineSections, section) {
				warnings = append(warnings, fmt.Sprintf(
					"%s: 段是终端直用引擎键，平台侧以配置中心与部署期环境变量为准；终端直用请放 %s",
					section, "~/.k8s-ai/cli.yaml"))
			}
		case ValidateModeCLI:
			if section == "platform" {
				warnings = append(warnings, "platform: 段是平台静态启动参数，由 server 读取配置文件；终端直用不消费")
			}
		}
	}

	if dsn, ok := leaves["platform.store.dsn"].(string); ok && strings.TrimSpace(dsn) != "" {
		warnings = append(warnings, "platform.store.dsn 在配置文件里非空：DSN 含口令，应只经 K8S_AI_PLATFORM_STORE_DSN / Secret 注入")
	}
	return warnings, nil
}

// misplacedStaticKeys 返回该引擎段下不在平台静态白名单里的键（已排序）。
func misplacedStaticKeys(section string, leaves map[string]any) []string {
	prefix := section + "."
	var out []string
	for key := range leaves {
		if !strings.HasPrefix(key, prefix) || platformStaticEngineKeys[key] {
			continue
		}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// shallowestUnknown 返回 key 链上第一个未识别的祖先键（父链已识别）。
func shallowestUnknown(key string, idx keyIndex) string {
	parts := strings.Split(key, ".")
	for i := range parts {
		cur := strings.Join(parts[:i+1], ".")
		if idx.known(cur) {
			continue
		}
		return cur
	}
	return ""
}

// flattenKeys 把 YAML 文档压成点分叶子键；map 字段整体作为前缀，不再下钻。
func flattenKeys(node map[string]any, prefix string, out map[string]any) {
	for rawKey, value := range node {
		key := rawKey
		if prefix != "" {
			key = prefix + "." + rawKey
		}
		if child, ok := asStringMap(value); ok && len(child) > 0 {
			flattenKeys(child, key, out)
			continue
		}
		out[key] = value
	}
}

func asStringMap(value any) (map[string]any, bool) {
	switch m := value.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[fmt.Sprint(k)] = v
		}
		return out, true
	default:
		return nil, false
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
