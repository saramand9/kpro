// File: internal/modules/reports/service/dto.go
// Purpose: Defines service-level DTOs for report operations.

package service

// BuildSummaryInput is the service input for building a project report summary.
type BuildSummaryInput struct {
	Project string
	Owner   string
}

// Summary is the service output returned by the report business module.
type Summary struct {
	Project string `json:"project"`
	Owner   string `json:"owner"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Items   int64  `json:"items"`
}
