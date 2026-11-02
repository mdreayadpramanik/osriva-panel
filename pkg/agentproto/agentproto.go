// Package agentproto defines the wire protocol between the OSRIVA Agent and
// the control plane (docs/adr/0004-agent-enrolment-and-mtls.md).
package agentproto

import (
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/inventory"
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
