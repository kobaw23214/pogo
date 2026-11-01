package observability

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OTLPOptionsFromEnv is separate from opening the exporter so both frontends
// can report configuration problems without disabling local observation.
func OTLPOptionsFromEnv(getenv func(string) string) (OTLPOptions, error) {
	if strings.EqualFold(strings.TrimSpace(getenv("OTEL_SDK_DISABLED")), "true") || getenv("POGO_OTLP") == "0" {
		return OTLPOptions{}, nil
	}
	o := OTLPOptions{Endpoint: strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")), ServiceName: strings.TrimSpace(getenv("OTEL_SERVICE_NAME"))}
	if o.Endpoint == "" {
		if base := strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); base != "" {
			u, err := url.Parse(base)
			if err != nil {
				return OTLPOptions{}, errors.New("invalid OTLP endpoint")
			}
			u.Path = strings.TrimRight(u.Path, "/") + "/v1/traces"
			o.Endpoint = u.String()
		}
	}
	if o.Endpoint == "" {
		return OTLPOptions{}, nil
	}
	protocol := strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"))
	if protocol == "" {
		protocol = strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))
	}
	if protocol != "" && protocol != "http/json" {
		return OTLPOptions{}, errors.New("Pogo OTLP exporter supports http/json; set OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/json")
	}
	headers := getenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS")
	if headers == "" {
		headers = getenv("OTEL_EXPORTER_OTLP_HEADERS")
	}
	o.Headers = map[string]string{}
	if headers != "" {
		if len(headers) > 16384 {
			return OTLPOptions{}, errors.New("OTLP headers exceed size limit")
		}
		for _, part := range strings.Split(headers, ",") {
			key, raw, ok := strings.Cut(strings.TrimSpace(part), "=")
			key = http.CanonicalHeaderKey(strings.TrimSpace(key))
			if !ok || !otlpHeaderName(key) || strings.Contains(raw, ";") {
				return OTLPOptions{}, errors.New("invalid OTLP headers")
			}
			value, err := url.PathUnescape(raw)
			if err != nil {
				return OTLPOptions{}, errors.New("invalid OTLP header encoding")
			}
			if _, exists := o.Headers[key]; exists {
				return OTLPOptions{}, errors.New("duplicate OTLP header")
			}
			o.Headers[key] = value
		}
	}
	timeout := getenv("OTEL_EXPORTER_OTLP_TRACES_TIMEOUT")
	if timeout == "" {
		timeout = getenv("OTEL_EXPORTER_OTLP_TIMEOUT")
	}
	if timeout != "" {
		ms, err := strconv.ParseInt(timeout, 10, 32)
		if err != nil || ms < 1 || ms > 60000 {
			return OTLPOptions{}, errors.New("OTLP timeout must be 1..60000 milliseconds")
		}
		o.Timeout = time.Duration(ms) * time.Millisecond
	}
	return o, nil
}
