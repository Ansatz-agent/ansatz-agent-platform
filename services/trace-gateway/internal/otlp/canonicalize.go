package otlp

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/auth"
	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/redact"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

type Headers struct {
	SessionID     string
	Entrypoint    string
	RunID         string
	SchemaVersion string
}

var canonicalKeys = map[string]struct{}{
	"platform.user.id":                   {},
	"user.id":                            {},
	"langfuse.user.id":                   {},
	"client.installation.id":             {},
	"hermes.session.id":                  {},
	"session.id":                         {},
	"langfuse.session.id":                {},
	"gen_ai.conversation.id":             {},
	"hermes.entrypoint":                  {},
	"hermes.run.id":                      {},
	"telemetry.schema.version":           {},
	"trace.gateway.request.id":           {},
	"langfuse.trace.input":               {},
	"langfuse.trace.output":              {},
	"langfuse.observation.type":          {},
	"langfuse.observation.input":         {},
	"langfuse.observation.output":        {},
	"langfuse.observation.model.name":    {},
	"langfuse.observation.usage_details": {},
	"langfuse.trace.metadata.username":   {},
}

func Canonicalize(
	request *collectortracepb.ExportTraceServiceRequest,
	principal auth.Principal,
	headers Headers,
	gatewayRequestID string,
) error {
	if request == nil || principal.UserID == "" || principal.Username == "" || principal.InstallationID == "" ||
		headers.SessionID == "" || headers.Entrypoint == "" || headers.RunID == "" ||
		headers.SchemaVersion != "1" || gatewayRequestID == "" {
		return errors.New("invalid canonical identity")
	}
	for _, resourceSpans := range request.ResourceSpans {
		if resourceSpans == nil {
			continue
		}
		if resourceSpans.Resource == nil {
			resourceSpans.Resource = &resourcepb.Resource{}
		}
		resourceSpans.Resource.Attributes = withoutCanonical(resourceSpans.Resource.Attributes)
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			if scopeSpans == nil {
				continue
			}
			if scopeSpans.Scope != nil {
				scopeSpans.Scope.Attributes = withoutCanonical(scopeSpans.Scope.Attributes)
			}
			for _, span := range scopeSpans.Spans {
				if span == nil {
					continue
				}
				span.Attributes = withoutCanonical(span.Attributes)
				span.Attributes = projectRelayForLangfuse(span.Name, span.Attributes)
				span.Attributes = append(
					span.Attributes,
					stringAttribute("user.id", principal.UserID),
					stringAttribute("langfuse.user.id", principal.UserID),
					stringAttribute("langfuse.trace.metadata.username", principal.Username),
					stringAttribute("session.id", headers.SessionID),
					stringAttribute("langfuse.session.id", headers.SessionID),
					stringAttribute("gen_ai.conversation.id", headers.SessionID),
				)
				for _, event := range span.Events {
					if event != nil {
						event.Attributes = withoutCanonical(event.Attributes)
					}
				}
				for _, link := range span.Links {
					if link != nil {
						link.Attributes = withoutCanonical(link.Attributes)
					}
				}
			}
		}
		resourceSpans.Resource.Attributes = append(
			resourceSpans.Resource.Attributes,
			stringAttribute("platform.user.id", principal.UserID),
			stringAttribute("user.id", principal.UserID),
			stringAttribute("langfuse.user.id", principal.UserID),
			stringAttribute("langfuse.trace.metadata.username", principal.Username),
			stringAttribute("client.installation.id", principal.InstallationID),
			stringAttribute("hermes.session.id", headers.SessionID),
			stringAttribute("session.id", headers.SessionID),
			stringAttribute("langfuse.session.id", headers.SessionID),
			stringAttribute("gen_ai.conversation.id", headers.SessionID),
			stringAttribute("hermes.entrypoint", headers.Entrypoint),
			stringAttribute("hermes.run.id", headers.RunID),
			stringAttribute("telemetry.schema.version", "1"),
			stringAttribute("trace.gateway.request.id", gatewayRequestID),
		)
	}
	return nil
}

func projectRelayForLangfuse(
	spanName string,
	attributes []*commonpb.KeyValue,
) []*commonpb.KeyValue {
	scopeType, _ := stringValue(attributes, "nemo_relay.scope_type")
	switch {
	case spanName == "hermes.turn":
		attributes = append(attributes, stringAttribute("langfuse.observation.type", "span"))
		if input, ok := stringValue(attributes, "nemo_relay.start.input.user"); ok {
			attributes = append(
				attributes,
				stringAttribute("langfuse.observation.input", input),
				stringAttribute("langfuse.trace.input", input),
			)
		}
		if output, ok := stringValue(attributes, "nemo_relay.end.output.assistant"); ok {
			attributes = append(
				attributes,
				stringAttribute("langfuse.observation.output", output),
				stringAttribute("langfuse.trace.output", output),
			)
		}
	case scopeType == "llm":
		attributes = append(attributes, stringAttribute("langfuse.observation.type", "generation"))
		if input, ok := preferredRelayPayload(
			attributes,
			"nemo_relay.start.input.content",
			"nemo_relay.start.input",
		); ok {
			attributes = append(attributes, stringAttribute("langfuse.observation.input", input))
		}
		if output, ok := preferredRelayPayload(
			attributes,
			"nemo_relay.end.output.choices",
			"nemo_relay.end.output",
		); ok {
			attributes = append(attributes, stringAttribute("langfuse.observation.output", output))
		}
		if model, ok := stringValue(attributes, "nemo_relay.model_name"); ok {
			attributes = append(attributes, stringAttribute("langfuse.observation.model.name", normalizeModelName(model)))
		}
		if usage, ok := relayUsageDetails(attributes); ok {
			attributes = append(attributes, stringAttribute("langfuse.observation.usage_details", usage))
		}
	case scopeType == "tool":
		attributes = append(attributes, stringAttribute("langfuse.observation.type", "span"))
		if input, ok := relayPayload(attributes, "nemo_relay.start.input"); ok {
			attributes = append(attributes, stringAttribute("langfuse.observation.input", input))
		}
		if output, ok := relayPayload(attributes, "nemo_relay.end.output"); ok {
			attributes = append(attributes, stringAttribute("langfuse.observation.output", output))
		}
	}
	return attributes
}

func normalizeModelName(model string) string {
	parts := strings.Split(model, "/")
	if len(parts) >= 3 && parts[0] != "" && parts[0] == parts[1] {
		return strings.Join(parts[1:], "/")
	}
	return model
}

func relayUsageDetails(attributes []*commonpb.KeyValue) (string, bool) {
	raw, ok := relayPayload(attributes, "nemo_relay.end.output.usage")
	if !ok {
		return "", false
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var usage map[string]any
	if err := decoder.Decode(&usage); err != nil {
		return "", false
	}

	input, hasInput := firstTokenCount(usage, []string{"prompt_tokens"}, []string{"input_tokens"})
	output, hasOutput := firstTokenCount(usage, []string{"completion_tokens"}, []string{"output_tokens"})
	total, hasTotal := firstTokenCount(usage, []string{"total_tokens"}, []string{"total"})
	cacheRead, hasCacheRead := firstTokenCount(
		usage,
		[]string{"prompt_tokens_details", "cached_tokens"},
		[]string{"input_tokens_details", "cached_tokens"},
		[]string{"cache_read_input_tokens"},
		[]string{"cache_read_tokens"},
	)
	cacheWrite, hasCacheWrite := firstTokenCount(
		usage,
		[]string{"prompt_tokens_details", "cache_creation_tokens"},
		[]string{"input_tokens_details", "cache_creation_tokens"},
		[]string{"cache_creation_input_tokens"},
		[]string{"cache_write_tokens"},
	)
	reasoning, hasReasoning := firstTokenCount(
		usage,
		[]string{"completion_tokens_details", "reasoning_tokens"},
		[]string{"output_tokens_details", "reasoning_tokens"},
		[]string{"reasoning_tokens"},
	)

	details := make(map[string]int64)
	if hasInput {
		regularInput := input
		_, hasPromptTokens := tokenCountAt(usage, []string{"prompt_tokens"})
		_, hasNestedCachedTokens := firstTokenCount(
			usage,
			[]string{"prompt_tokens_details", "cached_tokens"},
			[]string{"input_tokens_details", "cached_tokens"},
		)
		if hasPromptTokens || hasNestedCachedTokens {
			regularInput = subtractTokenDetails(regularInput, cacheRead, cacheWrite)
		}
		details["input"] = regularInput
	}
	if hasOutput {
		regularOutput := output
		if hasReasoning {
			regularOutput = subtractTokenDetails(regularOutput, reasoning)
		}
		details["output"] = regularOutput
	}
	if hasCacheRead {
		details["input_cached_tokens"] = cacheRead
	}
	if hasCacheWrite {
		details["input_cache_creation"] = cacheWrite
	}
	if hasReasoning {
		details["output_reasoning_tokens"] = reasoning
	}
	if hasTotal {
		details["total"] = total
	} else if len(details) > 0 {
		for key, value := range details {
			if key != "total" {
				total += value
			}
		}
		details["total"] = total
	}
	if len(details) == 0 {
		return "", false
	}
	rendered, err := json.Marshal(details)
	if err != nil {
		return "", false
	}
	return string(rendered), true
}

func firstTokenCount(value map[string]any, paths ...[]string) (int64, bool) {
	for _, path := range paths {
		if count, ok := tokenCountAt(value, path); ok {
			return count, true
		}
	}
	return 0, false
}

func tokenCountAt(value map[string]any, path []string) (int64, bool) {
	var current any = value
	for _, part := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return 0, false
		}
		current, ok = object[part]
		if !ok {
			return 0, false
		}
	}
	number, ok := current.(json.Number)
	if !ok {
		return 0, false
	}
	count, err := number.Int64()
	return count, err == nil && count >= 0
}

func subtractTokenDetails(total int64, details ...int64) int64 {
	for _, detail := range details {
		if detail >= total {
			return 0
		}
		total -= detail
	}
	return total
}

func preferredRelayPayload(
	attributes []*commonpb.KeyValue,
	preferredKey string,
	fallbackPrefix string,
) (string, bool) {
	if value := attributeValue(attributes, preferredKey); value != nil {
		return renderPayloadValue(value), true
	}
	return relayPayload(attributes, fallbackPrefix)
}

func relayPayload(
	attributes []*commonpb.KeyValue,
	prefix string,
) (string, bool) {
	if value := attributeValue(attributes, prefix); value != nil {
		return renderPayloadValue(value), true
	}
	values := make(map[string]any)
	nestedPrefix := prefix + "."
	for _, attribute := range attributes {
		if attribute == nil || !strings.HasPrefix(attribute.Key, nestedPrefix) {
			continue
		}
		key := strings.TrimPrefix(attribute.Key, nestedPrefix)
		if key == "" || attribute.Value == nil {
			continue
		}
		values[key] = payloadValue(attribute.Value)
	}
	if len(values) == 0 {
		return "", false
	}
	rendered, err := json.Marshal(values)
	if err != nil {
		return "", false
	}
	return string(rendered), true
}

func renderPayloadValue(value *commonpb.AnyValue) string {
	if value == nil {
		return ""
	}
	if stringValue, ok := value.Value.(*commonpb.AnyValue_StringValue); ok {
		return stringValue.StringValue
	}
	rendered, err := json.Marshal(payloadValue(value))
	if err != nil {
		return ""
	}
	return string(rendered)
}

func payloadValue(value *commonpb.AnyValue) any {
	if value == nil {
		return nil
	}
	switch typed := value.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		trimmed := strings.TrimSpace(typed.StringValue)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var decoded any
			if json.Unmarshal([]byte(trimmed), &decoded) == nil {
				return decoded
			}
		}
		return typed.StringValue
	case *commonpb.AnyValue_BoolValue:
		return typed.BoolValue
	case *commonpb.AnyValue_IntValue:
		return typed.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return typed.DoubleValue
	case *commonpb.AnyValue_ArrayValue:
		values := make([]any, 0, len(typed.ArrayValue.Values))
		for _, item := range typed.ArrayValue.Values {
			values = append(values, payloadValue(item))
		}
		return values
	case *commonpb.AnyValue_KvlistValue:
		values := make(map[string]any)
		for _, item := range typed.KvlistValue.Values {
			if item != nil {
				values[item.Key] = payloadValue(item.Value)
			}
		}
		return values
	case *commonpb.AnyValue_BytesValue:
		return typed.BytesValue
	default:
		return nil
	}
}

func stringValue(
	attributes []*commonpb.KeyValue,
	key string,
) (string, bool) {
	value := attributeValue(attributes, key)
	if value == nil {
		return "", false
	}
	stringValue, ok := value.Value.(*commonpb.AnyValue_StringValue)
	if !ok {
		return "", false
	}
	return stringValue.StringValue, true
}

func attributeValue(
	attributes []*commonpb.KeyValue,
	key string,
) *commonpb.AnyValue {
	for _, attribute := range attributes {
		if attribute != nil && attribute.Key == key {
			return attribute.Value
		}
	}
	return nil
}

func withoutCanonical(attributes []*commonpb.KeyValue) []*commonpb.KeyValue {
	clean := redact.Attributes(attributes)
	result := clean[:0]
	for _, attribute := range clean {
		if attribute == nil {
			continue
		}
		if _, reserved := canonicalKeys[attribute.Key]; reserved {
			continue
		}
		result = append(result, attribute)
	}
	return result
}

func stringAttribute(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key:   key,
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}},
	}
}
