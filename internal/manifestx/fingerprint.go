package manifestx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"gopkg.in/yaml.v3"
)

// serverSetFields 是服务端会回填/重写的元数据字段，不参与指纹计算，
// 保证 dry-run 提案与 apply 提交之间的指纹可比对（R3.5）。
var serverSetFields = []string{
	"uid", "resourceVersion", "generation", "creationTimestamp", "managedFields", "selfLink", "deletionTimestamp",
}

// Fingerprint 返回对象的 canonical SHA-256 指纹（形如 sha256:xxxx）。
// 字段顺序无关：encoding/json 对 map 键排序，数组保持原序（语义相关）。
func Fingerprint(content map[string]any) string {
	raw, err := json.Marshal(canonical(content))
	if err != nil {
		// 走到这里说明内容无法序列化；调用方已通过解析保证可序列化，
		// 这里退化为空串并由上层按"缺少指纹"拒绝执行。
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// canonical 返回去掉服务端字段后的深拷贝。
func canonical(content map[string]any) map[string]any {
	out := deepCopyMap(content)
	delete(out, "status")
	if meta, ok := out["metadata"].(map[string]any); ok {
		for _, f := range serverSetFields {
			delete(meta, f)
		}
		if len(meta) == 0 {
			delete(out, "metadata")
		}
	}
	if len(out) == 0 {
		out["__empty__"] = true
	}
	return out
}

// DisplayManifest 渲染给人看的 manifest：Secret 值替换为长度与指纹标记。
func DisplayManifest(content map[string]any, keys []SecretKeyFingerprint) string {
	view := canonical(content)
	if IsSecretKind(stringAt(content, "kind")) {
		view = redactSecret(content, keys)
		delete(view, "status")
	}
	raw, err := yaml.Marshal(view)
	if err != nil {
		return ""
	}
	return string(raw)
}

func stringAt(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
