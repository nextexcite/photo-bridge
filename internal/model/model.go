package model

import "time"

const ReportSchemaVersion = "photo-bridge.report/v1alpha1"

type ManifestEntry struct {
	Path          string    `json:"path"`
	Size          int64     `json:"size"`
	ModTime       time.Time `json:"modTime"`
	HashAlgorithm string    `json:"hashAlgorithm,omitempty"`
	Hash          string    `json:"hash,omitempty"`
}

type RunReport struct {
	SchemaVersion     string             `json:"schemaVersion"`
	RunID             string             `json:"runId"`
	Job               string             `json:"job"`
	Operation         string             `json:"operation"`
	Status            string             `json:"status"`
	StartedAt         time.Time          `json:"startedAt"`
	FinishedAt        time.Time          `json:"finishedAt"`
	SourceDriver      string             `json:"sourceDriver"`
	DestinationDriver string             `json:"destinationDriver"`
	NonDestructive    bool               `json:"nonDestructive"`
	DryRun            bool               `json:"dryRun"`
	Manifest          ManifestResult     `json:"manifest"`
	Transfer          CommandResult      `json:"transfer"`
	Verification      VerificationResult `json:"verification"`
	Selection         *SelectionResult   `json:"selection,omitempty"`
	Error             string             `json:"error,omitempty"`
}

type SelectionResult struct {
	Kind           string    `json:"kind"`
	Status         string    `json:"status"`
	ArchiveCount   int       `json:"archiveCount"`
	TotalBytes     int64     `json:"totalBytes"`
	ObservedAt     time.Time `json:"observedAt,omitempty"`
	UnchangedSince time.Time `json:"unchangedSince,omitempty"`
	ReadyAfter     time.Time `json:"readyAfter,omitempty"`
	ManualReady    bool      `json:"manualReady,omitempty"`
}

type ManifestResult struct {
	Requested string `json:"requested"`
	Level     string `json:"level"`
	Entries   int    `json:"entries"`
	Path      string `json:"path,omitempty"`
}

type CommandResult struct {
	Status   string `json:"status"`
	ExitCode int    `json:"exitCode"`
	LogPath  string `json:"logPath,omitempty"`
}

type VerificationResult struct {
	Requested string `json:"requested"`
	Method    string `json:"method"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exitCode"`
	LogPath   string `json:"logPath,omitempty"`
}

type Plan struct {
	Job               string `json:"job"`
	Operation         string `json:"operation"`
	SourceDriver      string `json:"sourceDriver"`
	DestinationDriver string `json:"destinationDriver"`
	Manifest          string `json:"manifest"`
	Verification      string `json:"verification"`
	Transfers         int    `json:"transfers"`
	Retries           int    `json:"retries"`
	NonDestructive    bool   `json:"nonDestructive"`
	Executor          string `json:"executor"`
	Selector          string `json:"selector,omitempty"`
}
