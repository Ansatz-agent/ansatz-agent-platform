package redact

import (
	"strings"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

func TestAttributesRemoveCredentialAndRawAudioContent(t *testing.T) {
	attributes := []*commonpb.KeyValue{
		{Key: "prompt", Value: testStringValue("complete semantic prompt")},
		{Key: "http.request.header.authorization", Value: testStringValue("Bearer provider-secret")},
		{Key: "tool.arguments", Value: testStringValue(`{"api_key":"tool-secret","query":"weather"}`)},
		{Key: "voice.transcript", Value: testStringValue("complete transcript")},
		{Key: "voice.audio.bytes", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BytesValue{BytesValue: []byte("raw-audio")}}},
		{Key: "debug", Value: testStringValue("-----BEGIN PRIVATE KEY----- secret")},
	}

	got := Attributes(attributes)
	serialized := attributesText(got)
	for _, wanted := range []string{"complete semantic prompt", "complete transcript", "weather"} {
		if !strings.Contains(serialized, wanted) {
			t.Fatalf("semantic value %q missing from %s", wanted, serialized)
		}
	}
	for _, forbidden := range []string{"provider-secret", "tool-secret", "raw-audio", "PRIVATE KEY"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("sensitive value %q survived in %s", forbidden, serialized)
		}
	}
}

func testStringValue(value string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}
}

func attributesText(attributes []*commonpb.KeyValue) string {
	var builder strings.Builder
	for _, attribute := range attributes {
		builder.WriteString(attribute.Key)
		builder.WriteString("=")
		builder.WriteString(attribute.Value.String())
	}
	return builder.String()
}
