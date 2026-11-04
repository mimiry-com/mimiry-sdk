package mimiry

import "time"

// The types below follow what api-compute actually sends and accepts, read from
// its source; where the published OpenAPI document disagrees, the source won.
//
// Presence is kept where the API gives it meaning. A pointer is used for a
// field whose ABSENCE says something different from its zero value — a
// provider name withheld is not a provider named "", an uncounted number of
// active sessions is not 0.

// SessionState is what a session is: its durable lifecycle stage.
type SessionState string

const (
	SessionSubmitted       SessionState = "submitted"
	SessionProvisioned     SessionState = "provisioned"
	SessionBooting         SessionState = "booting"
	SessionBootFailed      SessionState = "boot_failed"
	SessionSettingUp       SessionState = "setting_up"
	SessionSetupFailed     SessionState = "setup_failed"
	SessionPullingImage    SessionState = "pulling_image"
	SessionPullFailed      SessionState = "pull_failed"
	SessionStartFailed     SessionState = "start_failed"
	SessionRunning         SessionState = "running"
	SessionIdle            SessionState = "idle"
	SessionExited          SessionState = "exited"
	SessionTerminating     SessionState = "terminating"
	SessionProvisionFailed SessionState = "provision_failed"
	SessionTerminated      SessionState = "terminated"
)

// Session is a compute session.
type Session struct {
	ID    string       `json:"id"`
	Name  string       `json:"name"`
	OrgID string       `json:"org_id,omitempty"`
	State SessionState `json:"state"`
	// Operation is what is happening right now ("starting__pulling_image");
	// empty at a resting state.
	Operation string    `json:"operation,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt moves only on a change of State.
	UpdatedAt     time.Time      `json:"updated_at"`
	StartedAt     *time.Time     `json:"started_at,omitempty"`
	SSH           *SSHInfo       `json:"ssh,omitempty"`
	SSHEnabled    bool           `json:"ssh_enabled"`
	AutoTerminate *AutoTerminate `json:"auto_terminate,omitempty"`
	ResultStorage string         `json:"result_storage"`
	GPUType       *string        `json:"gpu_type,omitempty"`
	GPUCount      int            `json:"gpu_count,omitempty"`
	ExitCode      *int           `json:"exit_code,omitempty"`
	// StopReason is set when something other than the workload ended the
	// session: user_requested, timed_out, balance_depleted,
	// vendor_instance_gone or system_error.
	StopReason  *string         `json:"stop_reason,omitempty"`
	Error       *string         `json:"error,omitempty"`
	Billing     *SessionBilling `json:"billing,omitempty"`
	Events      []SessionEvent  `json:"events,omitempty"`
	EventsTotal int             `json:"events_total,omitempty"`
}

// SSHInfo is how to reach a running session.
type SSHInfo struct {
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port"`
	Username string `json:"username"`
}

// SessionBilling is what a session costs. Billing starts when the provider
// creates the machine (BilledFrom), not when the container starts.
type SessionBilling struct {
	HourlyRate float64 `json:"hourly_rate"`
	Currency   string  `json:"currency"`
	// Provider is absent unless the platform exposes provider names.
	Provider   *string    `json:"provider,omitempty"`
	BilledFrom *time.Time `json:"billed_from,omitempty"`
	// RunningCost and DurationSeconds are the cost and time so far while the
	// session is live; once it ends, DurationSeconds is what was billed.
	RunningCost     float64 `json:"running_cost"`
	DurationSeconds int     `json:"duration_seconds"`
	// FinalCost is what was charged, set when the session ends.
	FinalCost       *float64 `json:"final_cost,omitempty"`
	CommittedTotal  *float64 `json:"committed_total,omitempty"`
	ReservedAmount  *float64 `json:"reserved_amount,omitempty"`
	CheckpointCount *int     `json:"checkpoint_count,omitempty"`
}

// SessionEvent is one entry of a session's timeline.
type SessionEvent struct {
	Timestamp string         `json:"timestamp"`
	Event     string         `json:"event"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
}

// AutoTerminate says when a session ends on its own.
type AutoTerminate struct {
	// Mode is AutoTerminateOnComplete (the default), AutoTerminateOnSuccess
	// or AutoTerminateNever.
	Mode           string `json:"mode"`
	TimeoutSeconds int64  `json:"timeout_seconds,omitempty"`
}

const (
	AutoTerminateOnComplete = "on_complete"
	AutoTerminateOnSuccess  = "on_success"
	AutoTerminateNever      = "never"
)

// CreateSessionRequest is the body of a session create.
type CreateSessionRequest struct {
	Name            string            `json:"name"`
	Image           Image             `json:"image"`
	GPU             GPUSpec           `json:"gpu"`
	Memory          *MemorySpec       `json:"memory,omitempty"`
	Command         *string           `json:"command,omitempty"`
	EnvironmentVars map[string]string `json:"environment_vars,omitempty"`
	// OrgID attributes the session to an organisation. When omitted, a
	// session with volume mounts takes the caller's only organisation; one
	// without stays personal.
	OrgID         *string        `json:"org_id,omitempty"`
	SSHEnabled    *bool          `json:"ssh_enabled,omitempty"`
	SSHKeyID      *string        `json:"ssh_key_id,omitempty"`
	SSHPublicKey  *string        `json:"ssh_public_key,omitempty"`
	AutoTerminate *AutoTerminate `json:"auto_terminate,omitempty"`
	// MaxDuration, in seconds, applies only when AutoTerminate is nil.
	MaxDuration   *int64        `json:"max_duration,omitempty"`
	ResultStorage string        `json:"result_storage,omitempty"`
	VolumeMounts  []VolumeMount `json:"volume_mounts,omitempty"`
}

// Image is the container image a session runs.
type Image struct {
	URI string `json:"uri"`
}

// GPUSpec selects the machine. At least one selector must be set: Types, a
// criterion (Family, VRAMGB, FormFactor, Architecture), a bound, or Priority.
type GPUSpec struct {
	Types        []string `json:"types,omitempty"`
	Family       []string `json:"family,omitempty"`
	VRAMGB       []int    `json:"vram_gb,omitempty"`
	FormFactor   []string `json:"form_factor,omitempty"`
	Architecture []string `json:"architecture,omitempty"`

	// Count is the number of GPUs; 0 means 1.
	Count       int  `json:"count,omitempty"`
	MaxGPUCount *int `json:"max_gpu_count,omitempty"`
	MaxVRAMGB   *int `json:"max_vram_gb,omitempty"`
	MinVCPUs    *int `json:"min_vcpus,omitempty"`
	MaxVCPUs    *int `json:"max_vcpus,omitempty"`
	MinMemoryGB *int `json:"min_memory_gb,omitempty"`
	MaxMemoryGB *int `json:"max_memory_gb,omitempty"`
	// MinHourlyRate and MaxHourlyRate bound the price, EUR per hour,
	// inclusive. There is no exact form.
	MinHourlyRate *float64 `json:"min_hourly_rate,omitempty"`
	MaxHourlyRate *float64 `json:"max_hourly_rate,omitempty"`

	Provider *string `json:"provider,omitempty"`
	Location *string `json:"location,omitempty"`
	// Priority orders the candidates: PRICE, GPU, FAMILY, FORM_FACTOR, VRAM, ARCH.
	Priority    []string `json:"priority,omitempty"`
	MaxAttempts *int     `json:"max_attempts,omitempty"`
}

// MemorySpec is the older form of GPUSpec.MinMemoryGB, which wins when both are set.
type MemorySpec struct {
	MinGB int `json:"min_gb"`
}

// VolumeMount attaches an existing volume to a session.
type VolumeMount struct {
	VolumeName string `json:"volume_name"`
	MountPath  string `json:"mount_path"`
}

// Accepted is the answer to a request the platform carries out asynchronously.
type Accepted struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	StatusURL string `json:"status_url"`
}

// SessionLogs is the tail of a session's container output.
type SessionLogs struct {
	SessionID      string `json:"session_id"`
	Logs           string `json:"logs"`
	Truncated      bool   `json:"truncated"`
	LinesRequested int    `json:"lines_requested"`
}

// VolumeState is what a volume is.
type VolumeState string

const (
	VolumeSubmitted   VolumeState = "submitted"
	VolumeProvisioned VolumeState = "provisioned"
	VolumeFailed      VolumeState = "failed"
	VolumeDeleted     VolumeState = "deleted"
)

// Volume is a persistent block volume. Attachment is a relationship, not a
// state: an attached volume is provisioned with AttachedTo set.
type Volume struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	OrgID         string      `json:"org_id"`
	SizeGB        int         `json:"size_gb"`
	VolumeType    string      `json:"volume_type"`
	State         VolumeState `json:"state"`
	Operation     string      `json:"operation,omitempty"`
	Location      string      `json:"location,omitempty"`
	AttachedTo    string      `json:"attached_to,omitempty"`
	RatePerGBHour float64     `json:"rate_per_gb_hour"`
	CreatedBy     string      `json:"created_by,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	// Reason explains State failed.
	Reason string `json:"reason,omitempty"`
}

// CreateVolumeRequest is the body of a volume create.
type CreateVolumeRequest struct {
	// OrgID may be omitted when the caller belongs to exactly one organisation.
	OrgID  string `json:"org_id,omitempty"`
	Name   string `json:"name"`
	SizeGB int    `json:"size_gb"`
	// VolumeType is "nvme" (the default) or "hdd".
	VolumeType string `json:"volume_type,omitempty"`
	// Location should match the session that will mount the volume.
	Location string `json:"location,omitempty"`
}

// CatalogEntry is a GPU model the platform knows, whether or not it is in stock.
type CatalogEntry struct {
	Name          string   `json:"name"`
	DisplayName   string   `json:"display_name"`
	Family        string   `json:"family"`
	VRAMGB        int      `json:"vram_gb"`
	FormFactor    string   `json:"form_factor"`
	FP16TFLOPS    float64  `json:"fp16_tflops,omitempty"`
	Architectures []string `json:"architectures"`
}

// GPUModel is a model's availability and prices. The fields marked full are
// present only when AvailabilityOptions.Full is set.
type GPUModel struct {
	Name        string          `json:"name"`
	DisplayName string          `json:"display_name"`
	Family      string          `json:"family"`
	VRAMGB      int             `json:"vram_gb"`
	Available   bool            `json:"available"`
	Currency    string          `json:"currency"`
	Providers   []ProviderPrice `json:"providers"`
	SKUs        []SKU           `json:"skus"`

	FormFactor     string          `json:"form_factor,omitempty"`      // full
	FP16TFLOPS     float64         `json:"fp16_tflops,omitempty"`      // full
	PriceUpdatedAt *time.Time      `json:"price_updated_at,omitempty"` // full
	Configurations []Configuration `json:"configurations,omitempty"`   // full
}

// ProviderPrice is one provider's price for a model. Available false is a
// real price that cannot be bought at this moment.
type ProviderPrice struct {
	// Provider is absent unless the platform exposes provider names.
	Provider   *string  `json:"provider,omitempty"`
	HourlyRate float64  `json:"hourly_rate"`
	Locations  []string `json:"locations"`
	Available  bool     `json:"available"`
}

// SKU is one buyable machine shape of a model.
type SKU struct {
	SKU string `json:"sku"`
	// Architecture is the silicon generation, "-" when unknown.
	Architecture   string    `json:"architecture"`
	GPUCount       int       `json:"gpu_count"`
	VCPUs          int       `json:"vcpus"`
	MemoryGB       int       `json:"memory_gb"`
	Confidential   bool      `json:"confidential,omitempty"`
	Available      bool      `json:"available"`
	FromHourlyRate float64   `json:"from_hourly_rate"`
	Currency       string    `json:"currency"`
	Listings       []Listing `json:"listings"`
}

// Listing is where and at what price a SKU is offered.
type Listing struct {
	Provider     *string `json:"provider,omitempty"`
	Location     string  `json:"location,omitempty"`
	InstanceType string  `json:"instance_type,omitempty"`
	HourlyRate   float64 `json:"hourly_rate"`
	Available    bool    `json:"available"`
}

// Configuration is a machine configuration of a model (full detail only).
type Configuration struct {
	GPUCount  int      `json:"gpu_count"`
	VCPUs     int      `json:"vcpus"`
	MemoryGB  int      `json:"memory_gb"`
	Locations []string `json:"locations"`
	Available bool     `json:"available"`
}

// Balance is an account's credit.
type Balance struct {
	// AccountType is "user" or "org".
	AccountType      string  `json:"account_type"`
	AccountID        string  `json:"account_id"`
	Balance          float64 `json:"balance"`
	Reserved         float64 `json:"reserved"`
	Available        float64 `json:"available"`
	Currency         string  `json:"currency"`
	TotalDeposited   float64 `json:"total_deposited"`
	TotalSpent       float64 `json:"total_spent"`
	EstimatedBalance float64 `json:"estimated_balance"`
	// ActiveSessions is nil when the platform could not count them, which is
	// not the same as none.
	ActiveSessions *int64 `json:"active_sessions"`
	AccountKind    string `json:"account_kind,omitempty"`
}

// Transaction is one credit or debit.
type Transaction struct {
	ID           string  `json:"id"`
	AccountType  string  `json:"account_type"`
	AccountID    string  `json:"account_id"`
	Type         string  `json:"type"` // "credit" or "debit"
	Amount       float64 `json:"amount"`
	BalanceAfter float64 `json:"balance_after"`
	Currency     string  `json:"currency"`
	Description  string  `json:"description"`
	ResourceID   *string `json:"resource_id,omitempty"`
	// ResourceType is "session", "volume" or "none"; nil means the row was
	// never classified, which is not the same as "none".
	ResourceType *string   `json:"resource_type,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Quota is the caller's usage limits. A limit of 0 or less means unlimited.
type Quota struct {
	MaxConcurrentJobs  int        `json:"max_concurrent_jobs"`
	CurrentJobs        int        `json:"current_jobs"`
	MaxGPUHoursDaily   float64    `json:"max_gpu_hours_daily"`
	GPUHoursUsedToday  float64    `json:"gpu_hours_used_today"`
	MaxGPUHoursMonthly float64    `json:"max_gpu_hours_monthly"`
	GPUHoursUsedMonth  float64    `json:"gpu_hours_used_month"`
	ResetsAt           *time.Time `json:"resets_at,omitempty"`
	LastDailyReset     time.Time  `json:"last_daily_reset"`
	LastMonthlyReset   time.Time  `json:"last_monthly_reset"`
}
