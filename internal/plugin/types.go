package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
)

const (
	ABIVersion    uint32 = 1
	SchemaVersion uint32 = 1

	MethodPluginRegister    = "plugin.register"
	MethodPluginReconfigure = "plugin.reconfigure"

	// MethodSchedulerPick is the host->plugin call that asks this plugin to
	// choose an auth candidate among those available for a routed provider,
	// before the host's built-in scheduler runs. A native key binding narrows
	// the candidates to its exact auth IDs. Returning Handled=false falls back
	// to the host scheduler for unbound keys.
	MethodSchedulerPick = "scheduler.pick"

	// MethodUsageHandle is the host->plugin call that delivers a finalized
	// usage record (tokens already parsed by CPA) after a request completes,
	// streaming and non-streaming alike.
	MethodUsageHandle = "usage.handle"

	MethodManagementRegister = "management.register"
	MethodManagementHandle   = "management.handle"
)

const (
	// SchedulerCallerScopeMetadataKey is populated by CPA after its native
	// config-api-key provider authenticates a downstream key. The value is a
	// stable, one-way scope identifier rather than the plaintext API key.
	SchedulerCallerScopeMetadataKey = "caller_scope"
)

const (
	PluginID   = "access-guard"
	PluginName = "Access Guard"
)

// Version is stamped at build time via -ldflags "-X cpa-access-guard/internal/plugin.Version=<ver>".
// The default marks locally built binaries that skipped the release pipeline.
var Version = "0.0.0-dev"

type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *EnvelopeError  `json:"error,omitempty"`
}

type EnvelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type LifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type Registration struct {
	SchemaVersion uint32       `json:"schema_version"`
	Metadata      Metadata     `json:"metadata"`
	Capabilities  Capabilities `json:"capabilities"`
}

type Metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo,omitempty"`
	ConfigFields     []ConfigField `json:"ConfigFields"`
}

type ConfigField struct {
	Name        string   `json:"Name"`
	Type        string   `json:"Type"`
	EnumValues  []string `json:"EnumValues,omitempty"`
	Description string   `json:"Description"`
}

type Capabilities struct {
	// Receiving all available tiers lets authorization run before priority.
	SchedulerAcrossPriorities bool `json:"scheduler_across_priorities,omitempty"`
	Scheduler                 bool `json:"scheduler,omitempty"`
	UsagePlugin               bool `json:"usage_plugin"`
	ManagementAPI             bool `json:"management_api"`
}

// SchedulerPickRequest is the payload of the host->plugin scheduler.pick call.
// It mirrors only the parts of pluginapi.SchedulerPickRequest the plugin
// reads. Leaving out request headers and each candidate's Attributes and
// Metadata means the decoder skips them instead of allocating maps for every
// candidate on every request.
type SchedulerPickRequest struct {
	Provider   string                   `json:"Provider,omitempty"`
	Providers  []string                 `json:"Providers,omitempty"`
	Model      string                   `json:"Model"`
	Options    SchedulerPickOptions     `json:"Options"`
	Candidates []SchedulerAuthCandidate `json:"Candidates"`
}

type SchedulerPickOptions struct {
	// Metadata carries CPA's native caller_scope.
	Metadata map[string]any `json:"Metadata,omitempty"`
}

// SchedulerAuthCandidate describes one selectable auth record.
type SchedulerAuthCandidate struct {
	ID       string `json:"ID"`
	Provider string `json:"Provider"`
	Priority int    `json:"Priority,omitempty"`
	Status   string `json:"Status,omitempty"`
}

type SchedulerPickResponse struct {
	// AuthID picks a specific candidate; leave empty and set Handled=false to
	// defer to the host scheduler.
	AuthID string `json:"AuthID,omitempty"`
	// Handled reports whether the plugin made a scheduling decision.
	Handled bool `json:"Handled"`
}

// UsageHandleRequest is the payload of the host->plugin usage.handle call.
// CPA parses the token counts itself (from the upstream response, including
// the final usage frame of a stream) and delivers them here after a request
// completes — both streaming and non-streaming.
type UsageHandleRequest struct {
	// Model is the resolved upstream model id.
	Model string `json:"Model"`
	// Alias is the client-requested model name (what the caller passed in the
	// request body's "model" field). It is the pricing fallback and the
	// per-model usage bucket.
	Alias string `json:"Alias"`
	// APIKey is the client's native downstream key. It is hashed into the
	// caller scope that identifies the binding and never stored.
	APIKey string      `json:"APIKey"`
	Failed bool        `json:"Failed"`
	Detail UsageDetail `json:"Detail"`
}

// UsageDetail mirrors CPA's usage token breakdown. Only the fields we bill on.
type UsageDetail struct {
	InputTokens         int64 `json:"InputTokens"`
	OutputTokens        int64 `json:"OutputTokens"`
	ReasoningTokens     int64 `json:"ReasoningTokens"`
	CachedTokens        int64 `json:"CachedTokens"`
	CacheReadTokens     int64 `json:"CacheReadTokens"`
	CacheCreationTokens int64 `json:"CacheCreationTokens"`
	TotalTokens         int64 `json:"TotalTokens"`
}

// UsageHandleResponse is empty: usage.handle is a fire-and-forget notification.
type UsageHandleResponse struct{}

type ManagementRegistrationResponse struct {
	Routes    []ManagementRoute `json:"Routes"`
	Resources []ResourceRoute   `json:"Resources,omitempty"`
}

type ManagementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description,omitempty"`
}

// ResourceRoute declares a browser-navigable, unauthenticated GET resource that
// CPA serves under /v0/resource/plugins/<pluginID><Path>.
type ResourceRoute struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

type ManagementRequest struct {
	Method  string      `json:"Method"`
	Path    string      `json:"Path"`
	Headers http.Header `json:"Headers"`
	Query   url.Values  `json:"Query"`
	Body    []byte      `json:"Body"`
}

type ManagementResponse struct {
	StatusCode int         `json:"StatusCode,omitempty"`
	Headers    http.Header `json:"Headers,omitempty"`
	Body       []byte      `json:"Body,omitempty"`
}

func OKEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{OK: true, Result: raw})
}

func ErrorEnvelope(code, message string, status int) []byte {
	raw, _ := json.Marshal(Envelope{
		OK: false,
		Error: &EnvelopeError{
			Code:       code,
			Message:    message,
			HTTPStatus: status,
		},
	})
	return raw
}
