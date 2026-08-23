package redact

import (
	"encoding/json"
	"regexp"
	"strings"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

const replacement = "[REDACTED]"

var (
	separatorPattern = regexp.MustCompile(`[^a-z0-9]+`)
	credentialTerms  = []string{
		"authorization", "cookie", "setcookie", "csrf", "password", "passwd",
		"apikey", "accesskey", "secretkey", "accesstoken", "refreshtoken",
		"sessiontoken", "clientsecret", "privatekey", "langfusepublickey",
		"langfusesecretkey",
	}
	audioTerms = []string{"audiobytes", "audiobase64", "rawaudio", "microphonebytes", "audiofilepath"}
)

func Attributes(attributes []*commonpb.KeyValue) []*commonpb.KeyValue {
	return attributesAtDepth(attributes, 0)
}

func attributesAtDepth(attributes []*commonpb.KeyValue, depth int) []*commonpb.KeyValue {
	if depth > 12 {
		return nil
	}
	result := make([]*commonpb.KeyValue, 0, len(attributes))
	for index, attribute := range attributes {
		if index >= 256 || attribute == nil || attribute.Value == nil {
			break
		}
		normalized := normalizedKey(attribute.Key)
		if containsTerm(normalized, audioTerms) {
			continue
		}
		if containsTerm(normalized, credentialTerms) {
			result = append(result, &commonpb.KeyValue{Key: attribute.Key, Value: stringValue(replacement)})
			continue
		}
		result = append(result, &commonpb.KeyValue{Key: attribute.Key, Value: sanitizeValue(attribute.Value, depth+1)})
	}
	return result
}

func sanitizeValue(value *commonpb.AnyValue, depth int) *commonpb.AnyValue {
	if value == nil || depth > 12 {
		return stringValue(replacement)
	}
	switch typed := value.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return stringValue(sanitizeString(typed.StringValue, depth))
	case *commonpb.AnyValue_BytesValue:
		cloned := append([]byte(nil), typed.BytesValue...)
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BytesValue{BytesValue: cloned}}
	case *commonpb.AnyValue_ArrayValue:
		values := typed.ArrayValue.GetValues()
		if len(values) > 256 {
			values = values[:256]
		}
		clean := make([]*commonpb.AnyValue, 0, len(values))
		for _, item := range values {
			clean = append(clean, sanitizeValue(item, depth+1))
		}
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: clean}}}
	case *commonpb.AnyValue_KvlistValue:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_KvlistValue{KvlistValue: &commonpb.KeyValueList{
			Values: attributesAtDepth(typed.KvlistValue.GetValues(), depth+1),
		}}}
	default:
		return value
	}
}

func sanitizeString(value string, depth int) string {
	lowered := strings.ToLower(value)
	if strings.Contains(lowered, "-----begin ") && strings.Contains(lowered, "private key-----") {
		return replacement
	}
	if strings.HasPrefix(strings.TrimSpace(lowered), "bearer ") {
		return replacement
	}
	trimmed := strings.TrimSpace(value)
	if depth <= 12 && len(trimmed) <= 1024*1024 && (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
		var decoded any
		if json.Unmarshal([]byte(trimmed), &decoded) == nil {
			clean := sanitizeJSON(decoded, depth+1)
			if encoded, err := json.Marshal(clean); err == nil {
				return string(encoded)
			}
		}
	}
	return value
}

func sanitizeJSON(value any, depth int) any {
	if depth > 12 {
		return replacement
	}
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, min(len(typed), 256))
		count := 0
		for key, item := range typed {
			if count >= 256 {
				break
			}
			count++
			normalized := normalizedKey(key)
			if containsTerm(normalized, audioTerms) {
				continue
			}
			if containsTerm(normalized, credentialTerms) {
				clean[key] = replacement
			} else {
				clean[key] = sanitizeJSON(item, depth+1)
			}
		}
		return clean
	case []any:
		if len(typed) > 256 {
			typed = typed[:256]
		}
		clean := make([]any, len(typed))
		for index, item := range typed {
			clean[index] = sanitizeJSON(item, depth+1)
		}
		return clean
	case string:
		return sanitizeString(typed, depth+1)
	default:
		return value
	}
}

func normalizedKey(value string) string {
	return separatorPattern.ReplaceAllString(strings.ToLower(value), "")
}

func containsTerm(value string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

func stringValue(value string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}
}
