package otlp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/auth"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestCanonicalizeRemovesForgedIdentityAtEveryLevel(t *testing.T) {
	request := &collectortracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			kv("platform.user.id", "forged-resource"),
			kv("ansatz.account.id", "forged-account"),
			kv("langfuse.user.id", "forged-langfuse-user"),
			kv("session.id", "forged-session"),
			kv("langfuse.session.id", "forged-langfuse-session"),
			kv("gen_ai.conversation.id", "forged-conversation"),
			kv("langfuse.trace.metadata.username", "forged-username"),
			kv("prompt", "full prompt"),
		}},
		ScopeSpans: []*tracepb.ScopeSpans{{
			Scope: &commonpb.InstrumentationScope{Attributes: []*commonpb.KeyValue{
				kv("client.installation.id", "forged-scope"),
			}},
			Spans: []*tracepb.Span{{
				Name: "agent turn",
				Attributes: []*commonpb.KeyValue{
					kv("user.id", "forged-span"),
					kv("tool.result", "full tool result"),
					kv("authorization", "Bearer secret"),
				},
				Events: []*tracepb.Span_Event{{Attributes: []*commonpb.KeyValue{
					kv("trace.gateway.request.id", "forged-event"),
				}}},
			}},
		}},
	}}}
	principal := auth.Principal{
		TokenID:        "trusted-token",
		AccountID:      "22222222-2222-4222-8222-222222222222",
		UserID:         "42",
		Username:       "yiyuxiao",
		InstallationID: "11111111-1111-4111-8111-111111111111",
		Scope:          "trace:write",
		Audience:       "ansatz-trace-gateway",
	}
	headers := Headers{SessionID: "session-1", Entrypoint: "voice", RunID: "run-1", SchemaVersion: "1"}

	if err := Canonicalize(request, principal, headers, "11111111-1111-4111-8111-111111111111"); err != nil {
		t.Fatal(err)
	}

	resource := request.ResourceSpans[0].Resource.Attributes
	assertSingle(t, resource, "platform.user.id", principal.UserID)
	assertSingle(t, resource, "ansatz.account.id", principal.AccountID)
	assertSingle(t, resource, "user.id", principal.Username)
	assertSingle(t, resource, "langfuse.user.id", principal.Username)
	assertSingle(t, resource, "langfuse.trace.metadata.username", "yiyuxiao")
	assertSingle(t, resource, "client.installation.id", principal.InstallationID)
	assertSingle(t, resource, "hermes.session.id", "session-1")
	assertSingle(t, resource, "session.id", "session-1")
	assertSingle(t, resource, "langfuse.session.id", "session-1")
	assertSingle(t, resource, "gen_ai.conversation.id", "session-1")
	assertSingle(t, resource, "hermes.entrypoint", "voice")
	assertSingle(t, resource, "hermes.run.id", "run-1")
	assertSingle(t, resource, "telemetry.schema.version", "1")
	assertSingle(t, resource, "trace.gateway.batch.id", "11111111-1111-4111-8111-111111111111")

	span := request.ResourceSpans[0].ScopeSpans[0].Spans[0]
	assertSingle(t, span.Attributes, "user.id", principal.Username)
	assertSingle(t, span.Attributes, "langfuse.user.id", principal.Username)
	assertSingle(t, span.Attributes, "langfuse.trace.metadata.username", "yiyuxiao")
	assertSingle(t, span.Attributes, "session.id", "session-1")
	assertSingle(t, span.Attributes, "langfuse.session.id", "session-1")
	assertSingle(t, span.Attributes, "gen_ai.conversation.id", "session-1")

	all := request.String()
	for _, forged := range []string{
		"forged-resource",
		"forged-account",
		"forged-langfuse-user",
		"forged-session",
		"forged-langfuse-session",
		"forged-conversation",
		"forged-username",
		"forged-scope",
		"forged-span",
		"forged-event",
		"Bearer secret",
		"gateway-request-1",
	} {
		if strings.Contains(all, forged) {
			t.Fatalf("forged/sensitive value %q survived: %s", forged, all)
		}
	}
	for _, semantic := range []string{"full prompt", "full tool result"} {
		if !strings.Contains(all, semantic) {
			t.Fatalf("semantic value %q missing: %s", semantic, all)
		}
	}
}

func TestCanonicalizeProjectsRelayUsageIntoLangfuseGeneration(t *testing.T) {
	request := &collectortracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			Name: "openai",
			Attributes: []*commonpb.KeyValue{
				kv("nemo_relay.scope_type", "llm"),
				kv("langfuse.observation.usage_details", `{"input":999999}`),
				doubleKV("gen_ai.usage.cost", 999),
				doubleKV("nemo_relay.end.output.cost_usd", 0.0125),
				kv("nemo_relay.end.output.usage", `{
					"prompt_tokens": 27376,
					"completion_tokens": 2370,
					"total_tokens": 29746,
					"prompt_tokens_details": {"cached_tokens": 11432},
					"completion_tokens_details": {"reasoning_tokens": 302}
				}`),
			},
		}}}},
	}}}
	principal := auth.Principal{AccountID: "22222222-2222-4222-8222-222222222222", UserID: "42", Username: "yiyuxiao", InstallationID: "11111111-1111-4111-8111-111111111111"}
	headers := Headers{SessionID: "session-1", Entrypoint: "desktop", RunID: "run-1", SchemaVersion: "1"}

	if err := Canonicalize(request, principal, headers, "gateway-request-1"); err != nil {
		t.Fatal(err)
	}

	assertJSONEquivalent(
		t,
		attributeString(request.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes, "langfuse.observation.usage_details"),
		`{
			"input": 15944,
			"output": 2068,
			"total": 29746,
			"input_cached_tokens": 11432,
			"output_reasoning_tokens": 302
		}`,
	)
	assertSingleDouble(
		t,
		request.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes,
		"gen_ai.usage.cost",
		0.0125,
	)
}

func TestCanonicalizeProjectsLogicalAccountingOntoLatestPhysicalAttempt(t *testing.T) {
	logicalID := []byte("logical1")
	request := &collectortracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
			{
				SpanId: logicalID,
				Name:   "hermes.logical_llm_call",
				Attributes: []*commonpb.KeyValue{
					kv("nemo_relay.scope_type", "function"),
					kv("nemo_relay.end.output.usage", `{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}`),
					doubleKV("nemo_relay.end.output.cost_usd", 0.00125),
				},
			},
			{
				SpanId:          []byte("attempt1"),
				ParentSpanId:    logicalID,
				EndTimeUnixNano: 20,
				Name:            "deepseek",
				Attributes:      []*commonpb.KeyValue{kv("nemo_relay.scope_type", "llm")},
			},
			{
				SpanId:          []byte("attempt2"),
				ParentSpanId:    logicalID,
				EndTimeUnixNano: 30,
				Name:            "deepseek",
				Attributes:      []*commonpb.KeyValue{kv("nemo_relay.scope_type", "llm")},
			},
		}}},
	}}}
	principal := auth.Principal{AccountID: "22222222-2222-4222-8222-222222222222", UserID: "42", Username: "wangzihe", InstallationID: "11111111-1111-4111-8111-111111111111"}
	headers := Headers{SessionID: "session-1", Entrypoint: "desktop", RunID: "run-1", SchemaVersion: "1"}

	if err := Canonicalize(request, principal, headers, "gateway-request-1"); err != nil {
		t.Fatal(err)
	}

	spans := request.ResourceSpans[0].ScopeSpans[0].Spans
	if got := attributeString(spans[1].Attributes, "langfuse.observation.usage_details"); got != "" {
		t.Fatalf("older physical attempt unexpectedly received usage: %s", got)
	}
	assertJSONEquivalent(t, attributeString(spans[2].Attributes, "langfuse.observation.usage_details"), `{"input":11,"output":7,"total":18}`)
	assertSingleDouble(t, spans[2].Attributes, "gen_ai.usage.cost", 0.00125)
}

func TestCanonicalizeProjectsCompleteRelayConversationIntoLangfuse(t *testing.T) {
	request := &collectortracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
			{
				Name: "hermes.turn",
				Attributes: []*commonpb.KeyValue{
					kv("nemo_relay.scope_type", "function"),
					kv("nemo_relay.start.input.user", "完整用户问题"),
					kv("nemo_relay.end.output.assistant", "完整助手回复"),
					kv("langfuse.observation.input", "forged input"),
				},
			},
			{
				Name: "nvidia",
				Attributes: []*commonpb.KeyValue{
					kv("nemo_relay.scope_type", "llm"),
					kv("nemo_relay.start.input.content", `{"messages":[{"role":"user","content":"完整用户问题"}]}`),
					kv("nemo_relay.end.output.choices", `[{"message":{"role":"assistant","content":"完整助手回复"}}]`),
					kv("nemo_relay.model_name", "openai/openai/gpt-5.5"),
				},
			},
			{
				Name: "terminal",
				Attributes: []*commonpb.KeyValue{
					kv("nemo_relay.scope_type", "tool"),
					kv("nemo_relay.start.input.command", "printf complete"),
					kv("nemo_relay.start.input.options", `{"cwd":"/workspace","timeout":30}`),
					kv("nemo_relay.end.output.stdout", "complete"),
					intKV("nemo_relay.end.output.exit_code", 0),
				},
			},
		}}},
	}}}
	principal := auth.Principal{AccountID: "22222222-2222-4222-8222-222222222222", UserID: "42", Username: "yiyuxiao", InstallationID: "11111111-1111-4111-8111-111111111111"}
	headers := Headers{SessionID: "session-1", Entrypoint: "desktop", RunID: "run-1", SchemaVersion: "1"}

	if err := Canonicalize(request, principal, headers, "gateway-request-1"); err != nil {
		t.Fatal(err)
	}

	spans := request.ResourceSpans[0].ScopeSpans[0].Spans
	assertSingle(t, spans[0].Attributes, "langfuse.observation.type", "span")
	assertSingle(t, spans[0].Attributes, "langfuse.observation.input", "完整用户问题")
	assertSingle(t, spans[0].Attributes, "langfuse.observation.output", "完整助手回复")
	assertSingle(t, spans[0].Attributes, "langfuse.trace.input", "完整用户问题")
	assertSingle(t, spans[0].Attributes, "langfuse.trace.output", "完整助手回复")

	assertSingle(t, spans[1].Attributes, "langfuse.observation.type", "generation")
	assertJSONEquivalent(t, attributeString(spans[1].Attributes, "langfuse.observation.input"), `{"messages":[{"role":"user","content":"完整用户问题"}]}`)
	assertJSONEquivalent(t, attributeString(spans[1].Attributes, "langfuse.observation.output"), `[{"message":{"role":"assistant","content":"完整助手回复"}}]`)
	assertSingle(t, spans[1].Attributes, "langfuse.observation.model.name", "openai/gpt-5.5")

	assertSingle(t, spans[2].Attributes, "langfuse.observation.type", "span")
	assertJSONEquivalent(t, attributeString(spans[2].Attributes, "langfuse.observation.input"), `{"command":"printf complete","options":{"cwd":"/workspace","timeout":30}}`)
	assertJSONEquivalent(t, attributeString(spans[2].Attributes, "langfuse.observation.output"), `{"exit_code":0,"stdout":"complete"}`)
}

func kv(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func intKV(key string, value int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: value}}}
}

func doubleKV(key string, value float64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: value}}}
}

func attributeString(attributes []*commonpb.KeyValue, key string) string {
	for _, attribute := range attributes {
		if attribute.Key == key {
			return attribute.Value.GetStringValue()
		}
	}
	return ""
}

func assertJSONEquivalent(t *testing.T, got, want string) {
	t.Helper()
	var gotValue any
	var wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("invalid JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("invalid expected JSON %q: %v", want, err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON = %s, want %s", gotJSON, wantJSON)
	}
}

func assertSingle(t *testing.T, attributes []*commonpb.KeyValue, key, value string) {
	t.Helper()
	count := 0
	for _, attribute := range attributes {
		if attribute.Key == key {
			count++
			if attribute.Value.GetStringValue() != value {
				t.Fatalf("%s = %q, want %q", key, attribute.Value.GetStringValue(), value)
			}
		}
	}
	if count != 1 {
		t.Fatalf("%s count = %d, want 1", key, count)
	}
}

func assertSingleDouble(t *testing.T, attributes []*commonpb.KeyValue, key string, value float64) {
	t.Helper()
	count := 0
	for _, attribute := range attributes {
		if attribute.Key == key {
			count++
			if attribute.Value.GetDoubleValue() != value {
				t.Fatalf("%s = %v, want %v", key, attribute.Value.GetDoubleValue(), value)
			}
		}
	}
	if count != 1 {
		t.Fatalf("%s count = %d, want 1", key, count)
	}
}
