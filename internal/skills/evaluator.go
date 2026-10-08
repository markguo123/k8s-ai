package skills

import (
	"strings"
)

// walkPath resolves one or more dot-separated paths (comma separated) into the
// non-nil values they reach.
// A segment with a [] suffix iterates over the slice/map at that point before
// continuing with the remaining segments. Example:
// spec.template.spec.containers[].image.
func walkPath(obj any, path string) []any {
	if strings.TrimSpace(path) == "" {
		if obj != nil {
			return []any{obj}
		}
		return nil
	}
	var out []any
	for _, one := range strings.Split(path, ",") {
		one = strings.TrimSpace(one)
		if one == "" {
			continue
		}
		out = append(out, walkSegments(obj, strings.Split(one, "."))...)
	}
	return out
}

func walkSegments(cur any, segs []string) []any {
	if len(segs) == 0 {
		if cur == nil {
			return nil
		}
		return []any{cur}
	}
	seg := segs[0]
	iterate := false
	key := seg
	if strings.HasSuffix(seg, "[]") {
		iterate = true
		key = strings.TrimSuffix(seg, "[]")
	}
	if key == "*" {
		iterate = true
		key = ""
	}
	switch v := cur.(type) {
	case map[string]any:
		val, ok := v[key]
		if !ok {
			return nil
		}
		if iterate {
			return walkIterated(val, segs[1:])
		}
		return walkSegments(val, segs[1:])
	case []any:
		// Reaching a slice without an explicit [] marker is treated as a
		// lenient map-over at this level.
		var out []any
		for _, item := range v {
			out = append(out, walkSegments(item, segs)...)
		}
		return out
	default:
		return nil
	}
}

func walkIterated(val any, segs []string) []any {
	var out []any
	switch v := val.(type) {
	case []any:
		for _, item := range v {
			out = append(out, walkSegments(item, segs)...)
		}
	case map[string]any:
		for _, item := range v {
			out = append(out, walkSegments(item, segs)...)
		}
	}
	return out
}

func matchCondition(vals []any, cond string) bool {
	switch cond {
	case "missing":
		return len(vals) == 0
	case "exists", "present":
		return len(vals) > 0
	case "empty":
		if len(vals) == 0 {
			return true
		}
		for _, v := range vals {
			if !isEmptyValue(v) {
				return false
			}
		}
		return true
	case "true", "privileged":
		for _, v := range vals {
			if b, ok := v.(bool); ok && b {
				return true
			}
		}
		return false
	case "latest_or_empty":
		if len(vals) == 0 {
			return true
		}
		for _, v := range vals {
			s := toStringValue(v)
			if s == "" || isUnpinnedImage(s) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func isEmptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	case bool:
		return !t
	case int64:
		return t == 0
	case int:
		return t == 0
	case float64:
		return t == 0
	default:
		return false
	}
}

func toStringValue(v any) string {
	s, _ := v.(string)
	return s
}

func isUnpinnedImage(image string) bool {
	image = strings.TrimSpace(image)
	if image == "" {
		return true
	}
	lastSlash := strings.LastIndex(image, "/")
	name := image
	if lastSlash >= 0 {
		name = image[lastSlash+1:]
	}
	lastColon := strings.LastIndex(name, ":")
	if lastColon < 0 {
		return true
	}
	tag := name[lastColon+1:]
	return strings.EqualFold(strings.TrimSpace(tag), "latest")
}
