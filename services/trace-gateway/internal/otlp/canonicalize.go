package otlp

import (
	"errors"

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
	"platform.user.id":         {},
	"user.id":                  {},
	"client.installation.id":   {},
	"hermes.session.id":        {},
	"hermes.entrypoint":        {},
	"hermes.run.id":            {},
	"telemetry.schema.version": {},
	"trace.gateway.request.id": {},
}

func Canonicalize(
	request *collectortracepb.ExportTraceServiceRequest,
	principal auth.Principal,
	headers Headers,
	gatewayRequestID string,
) error {
	if request == nil || principal.UserID == "" || principal.InstallationID == "" ||
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
			stringAttribute("client.installation.id", principal.InstallationID),
			stringAttribute("hermes.session.id", headers.SessionID),
			stringAttribute("hermes.entrypoint", headers.Entrypoint),
			stringAttribute("hermes.run.id", headers.RunID),
			stringAttribute("telemetry.schema.version", "1"),
			stringAttribute("trace.gateway.request.id", gatewayRequestID),
		)
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
