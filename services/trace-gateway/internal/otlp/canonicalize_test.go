package otlp

import (
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
		UserID:         "42",
		InstallationID: "11111111-1111-4111-8111-111111111111",
		Scope:          "trace:write",
		Audience:       "ansatz-trace-gateway",
	}
	headers := Headers{SessionID: "session-1", Entrypoint: "voice", RunID: "run-1", SchemaVersion: "1"}

	if err := Canonicalize(request, principal, headers, "gateway-request-1"); err != nil {
		t.Fatal(err)
	}

	resource := request.ResourceSpans[0].Resource.Attributes
	assertSingle(t, resource, "platform.user.id", "42")
	assertSingle(t, resource, "user.id", "42")
	assertSingle(t, resource, "client.installation.id", principal.InstallationID)
	assertSingle(t, resource, "hermes.session.id", "session-1")
	assertSingle(t, resource, "hermes.entrypoint", "voice")
	assertSingle(t, resource, "hermes.run.id", "run-1")
	assertSingle(t, resource, "telemetry.schema.version", "1")
	assertSingle(t, resource, "trace.gateway.request.id", "gateway-request-1")

	all := request.String()
	for _, forged := range []string{"forged-resource", "forged-scope", "forged-span", "forged-event", "Bearer secret"} {
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

func kv(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
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
