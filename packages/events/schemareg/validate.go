package schemareg

import (
	"fmt"
	"regexp"
)

// Validate checks value v against a JSON Schema subset:
// type, required, properties, items, enum, additionalProperties,
// minimum/maximum, minLength/maxLength, pattern. Unknown keywords and
// "format" are ignored (annotation-only in our schemas). path is the
// display root (e.g. "$"). Returns a list of human-readable problems.
func Validate(schema Schema, v any, path string) []string {
	var errs []string

	if enum := toStrings(schema["enum"]); len(enum) > 0 {
		s, isStr := v.(string)
		if !isStr || !stringSet(schema["enum"])[s] {
			errs = append(errs, fmt.Sprintf("%s: value %v not in enum %v", path, v, enum))
			return errs
		}
	}

	if t := schema["type"]; t != nil {
		if !typeMatches(t, v) {
			errs = append(errs, fmt.Sprintf("%s: expected type %v, got %s", path, t, jsonType(v)))
			return errs
		}
	}

	switch val := v.(type) {
	case map[string]any:
		for _, req := range toStrings(schema["required"]) {
			if _, ok := val[req]; !ok {
				errs = append(errs, fmt.Sprintf("%s: missing required field %q", path, req))
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for name, pv := range val {
			if ps, ok := props[name]; ok {
				if pm, ok := ps.(map[string]any); ok {
					errs = append(errs, Validate(pm, pv, path+"."+name)...)
				}
			} else if ap, ok := schema["additionalProperties"].(bool); ok && !ap {
				errs = append(errs, fmt.Sprintf("%s: additional property %q not allowed", path, name))
			}
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for i, e := range val {
				errs = append(errs, Validate(items, e, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	case string:
		if n, ok := toFloat(schema["minLength"]); ok && float64(len(val)) < n {
			errs = append(errs, fmt.Sprintf("%s: string shorter than minLength %v", path, n))
		}
		if n, ok := toFloat(schema["maxLength"]); ok && float64(len(val)) > n {
			errs = append(errs, fmt.Sprintf("%s: string longer than maxLength %v", path, n))
		}
		if p, ok := schema["pattern"].(string); ok {
			if re, err := regexp.Compile(p); err == nil && !re.MatchString(val) {
				errs = append(errs, fmt.Sprintf("%s: does not match pattern %q", path, p))
			}
		}
	case float64:
		if n, ok := toFloat(schema["minimum"]); ok && val < n {
			errs = append(errs, fmt.Sprintf("%s: below minimum %v", path, n))
		}
		if n, ok := toFloat(schema["maximum"]); ok && val > n {
			errs = append(errs, fmt.Sprintf("%s: above maximum %v", path, n))
		}
	}
	return errs
}

func typeMatches(t any, v any) bool {
	switch ts := t.(type) {
	case string:
		return typeOneMatches(ts, v)
	case []any:
		for _, e := range ts {
			if s, ok := e.(string); ok && typeOneMatches(s, v) {
				return true
			}
		}
	}
	return true
}

func typeOneMatches(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	case "number":
		_, ok := v.(float64)
		return ok
	case "null":
		return v == nil
	}
	return true
}

func jsonType(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
