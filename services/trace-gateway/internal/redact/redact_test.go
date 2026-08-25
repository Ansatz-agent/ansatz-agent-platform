package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func TestAttributesReplaceBytesAndBase64DataURLsWithMediaDescriptors(t *testing.T) {
	bytesValue := []byte("raw-image-bytes")
	dataURLBytes := []byte("raw-audio-bytes")
	attributes := Attributes([]*commonpb.KeyValue{
		{Key: "tool.image", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BytesValue{BytesValue: bytesValue}}},
		{Key: "tool.audio", Value: testStringValue("data:audio/wav;base64,cmF3LWF1ZGlvLWJ5dGVz")},
		{Key: "tool.arguments", Value: testStringValue(`{"attachment":"data:image/png;base64,cmF3LWltYWdlLWJ5dGVz"}`)},
	})

	assertMediaDescriptor(t, attributeString(t, attributes, "tool.image"), "application/octet-stream", bytesValue)
	assertMediaDescriptor(t, attributeString(t, attributes, "tool.audio"), "audio/wav", dataURLBytes)
	var nested map[string]string
	if err := json.Unmarshal([]byte(attributeString(t, attributes, "tool.arguments")), &nested); err != nil {
		t.Fatal(err)
	}
	assertMediaDescriptor(t, nested["attachment"], "image/png", bytesValue)

	serialized := attributesText(attributes)
	for _, forbidden := range []string{"raw-image-bytes", "raw-audio-bytes", "cmF3LWF1ZGlvLWJ5dGVz"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("media content %q survived in %s", forbidden, serialized)
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

func attributeString(t *testing.T, attributes []*commonpb.KeyValue, key string) string {
	t.Helper()
	for _, attribute := range attributes {
		if attribute.Key == key {
			return attribute.Value.GetStringValue()
		}
	}
	t.Fatalf("attribute %q missing", key)
	return ""
}

func assertMediaDescriptor(t *testing.T, value, mediaType string, raw []byte) {
	t.Helper()
	var descriptor struct {
		Redacted  bool   `json:"redacted"`
		MediaType string `json:"media_type"`
		ByteSize  int    `json:"byte_size"`
		SHA256    string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(value), &descriptor); err != nil {
		t.Fatalf("invalid media descriptor %q: %v", value, err)
	}
	digest := sha256.Sum256(raw)
	if !descriptor.Redacted || descriptor.MediaType != mediaType || descriptor.ByteSize != len(raw) || descriptor.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("descriptor = %+v", descriptor)
	}
}
