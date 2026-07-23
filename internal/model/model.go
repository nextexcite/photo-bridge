package model

import "time"

const ReportSchemaVersion = "photo-bridge.report/v1alpha2"

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
	Phase             string             `json:"phase,omitempty"`
	StartedAt         time.Time          `json:"startedAt"`
	UpdatedAt         time.Time          `json:"updatedAt,omitempty"`
	FinishedAt        time.Time          `json:"finishedAt"`
	SourceDriver      string             `json:"sourceDriver"`
	DestinationDriver string             `json:"destinationDriver"`
	NonDestructive    bool               `json:"nonDestructive"`
	DryRun            bool               `json:"dryRun"`
	Manifest          ManifestResult     `json:"manifest"`
	Transfer          CommandResult      `json:"transfer"`
	Verification      VerificationResult `json:"verification"`
	Selection         *SelectionResult   `json:"selection,omitempty"`
	Progress          *Progress          `json:"progress,omitempty"`
	Retention         RetentionResult    `json:"retention,omitempty"`
	Error             string             `json:"error,omitempty"`
}
type Progress struct {
	Percent     float64   `json:"percent,omitempty"`
	Transferred int64     `json:"transferred,omitempty"`
	Total       int64     `json:"total,omitempty"`
	Speed       float64   `json:"speed,omitempty"`
	ETASeconds  *int64    `json:"etaSeconds,omitempty"`
	Errors      int64     `json:"errors,omitempty"`
	LastUpdated time.Time `json:"lastUpdated"`
}
type RetentionResult struct {
	Status         string `json:"status,omitempty"`
	DeletedRuns    int    `json:"deletedRuns,omitempty"`
	ReclaimedBytes int64  `json:"reclaimedBytes,omitempty"`
	Warning        string `json:"warning,omitempty"`
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
	Requested     string `json:"requested"`
	Method        string `json:"method"`
	HashAlgorithm string `json:"hashAlgorithm,omitempty"`
	Fallback      bool   `json:"fallback,omitempty"`
	Strength      string `json:"strength,omitempty"`
	Status        string `json:"status"`
	ExitCode      int    `json:"exitCode"`
	LogPath       string `json:"logPath,omitempty"`
}
type Plan struct {
	Job                    string `json:"job"`
	Operation              string `json:"operation"`
	SourceDriver           string `json:"sourceDriver"`
	DestinationDriver      string `json:"destinationDriver"`
	Manifest               string `json:"manifest"`
	Verification           string `json:"verification"`
	Transfers              int    `json:"transfers"`
	Checkers               int    `json:"checkers"`
	BufferSize             string `json:"bufferSize"`
	MaxBufferMemory        string `json:"maxBufferMemory"`
	EffectiveBufferMemory  int64  `json:"effectiveBufferMemoryBytes"`
	EffectiveTransfers     int    `json:"effectiveTransfers"`
	MaxDuration            string `json:"maxDuration"`
	MaxFiles               int64  `json:"maxFiles"`
	MaxBytes               int64  `json:"maxBytes"`
	ProgressInterval       string `json:"progressInterval"`
	NonDestructive         bool   `json:"nonDestructive"`
	Executor               string `json:"executor"`
	Selector               string `json:"selector,omitempty"`
	DataPath               string `json:"dataPath"`
	ServerSideCopy         bool   `json:"serverSideCopy"`
	VerificationResolution string `json:"verificationResolution"`
}
