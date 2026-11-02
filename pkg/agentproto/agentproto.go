// Package agentproto defines the wire protocol between the OSRIVA Agent and
// the control plane (docs/adr/0004-agent-enrolment-and-mtls.md).
package agentproto

import (
	"encoding/json"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/inventory"
	"github.com/mdreayadpramanik/osriva-panel/pkg/metrics"
)

const (
	// EnrollPath is served by the public panel API (through the reverse proxy).
	EnrollPath = "/api/v1/agent/enroll"
	// CAPath serves the public agent CA certificate (no authentication).
	CAPath = "/api/v1/agent/ca"
	// ReportPath and RenewPath are served by the mutual-TLS agent listener.
	ReportPath = "/agent/v1/report"
	RenewPath  = "/agent/v1/renew"

	// DefaultReportInterval is how often agents report. A server is considered
	// offline after OfflineAfter without a report.
	DefaultReportInterval = 60 * time.Second
	OfflineAfter          = 3 * DefaultReportInterval

	// MaxReportBytes bounds report bodies accepted by the control plane.
	MaxReportBytes = 1 << 20
)

type EnrollRequest struct {
	Token        string `json:"token"`
	CSRPEM       string `json:"csr_pem"`
	AgentVersion string `json:"agent_version"`
}

type EnrollResponse struct {
	ServerID       string `json:"server_id"`
	ServerName     string `json:"server_name"`
	CertificatePEM string `json:"certificate_pem"`
	CAPEM          string `json:"ca_pem"`
	// AgentEndpoint is the https:// base URL of the mutual-TLS agent listener.
	AgentEndpoint string `json:"agent_endpoint"`
}

type ReportRequest struct {
	AgentVersion string               `json:"agent_version"`
	Inventory    *inventory.Inventory `json:"inventory"`
}

type ReportResponse struct {
	NextReportSeconds int `json:"next_report_seconds"`
}

type RenewRequest struct {
	CSRPEM string `json:"csr_pem"`
}

type RenewResponse struct {
	CertificatePEM string `json:"certificate_pem"`
}

// Error matches the control plane's JSON error envelope.
type Error struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type CAResponse struct {
	CAPEM       string `json:"ca_pem"`
	Fingerprint string `json:"fingerprint"`
}

// ---- tasks and metrics (Phase 3, ADR-0005) ----

const (
	// TasksPollPath is a long poll: the server answers when tasks are queued or after PollWait.
	TasksPollPath = "/agent/v1/tasks/poll"
	// TaskResultPathPrefix + "{id}/result" receives a task outcome.
	TaskResultPathPrefix = "/agent/v1/tasks/"
	MetricsPath          = "/agent/v1/metrics"

	PollWait        = 25 * time.Second
	MaxResultBytes  = 1 << 20
	MaxMetricsBatch = 120
)

type TaskEnvelope struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params"`
}

type PollResponse struct {
	Tasks []TaskEnvelope `json:"tasks"`
}

type TaskResult struct {
	// Status is "succeeded" or "failed".
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type MetricsRequest struct {
	Samples []metrics.Sample `json:"samples"`
}
