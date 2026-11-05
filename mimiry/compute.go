package mimiry

import (
	"context"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const computeAPI = "/api/compute/v1"

// Services. Each is a view on the Client and costs nothing to hold.
type (
	// SessionsService manages compute sessions.
	SessionsService struct{ c *Client }
	// VolumesService manages persistent block volumes.
	VolumesService struct{ c *Client }
	// GPUsService reads the GPU catalogue and live availability.
	GPUsService struct{ c *Client }
	// AccountService reads balances, transactions and quota.
	AccountService struct{ c *Client }
)

// Sessions is the session API.
func (c *Client) Sessions() *SessionsService { return &SessionsService{c} }

// Volumes is the volume API.
func (c *Client) Volumes() *VolumesService { return &VolumesService{c} }

// GPUs is the catalogue and availability API.
func (c *Client) GPUs() *GPUsService { return &GPUsService{c} }

// Account is the balance, transaction and quota API.
func (c *Client) Account() *AccountService { return &AccountService{c} }

// ---- sessions ----

// Create submits a session. The platform provisions it asynchronously: the
// answer carries the new session's ID, and Get or WaitReady follow it.
func (s *SessionsService) Create(ctx context.Context, req CreateSessionRequest) (*Accepted, error) {
	var out Accepted
	if err := s.c.Do(ctx, http.MethodPost, computeAPI+"/sessions", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SessionListOptions filters a session list. Filters within one field are ORed.
type SessionListOptions struct {
	ListOptions
	State    []SessionState `json:"state,omitempty"`
	StateNot []SessionState `json:"state_not,omitempty"`
	// Operation and OperationNot take a primary operation: provisioning,
	// starting, stopping or terminating.
	Operation    []string `json:"operation,omitempty"`
	OperationNot []string `json:"operation_not,omitempty"`
	OrgID        string   `json:"org_id,omitempty"`
	// UpdatedAfter is inclusive, UpdatedBefore exclusive.
	UpdatedAfter  time.Time `json:"updated_after,omitzero"`
	UpdatedBefore time.Time `json:"updated_before,omitzero"`
}

func (o SessionListOptions) query() url.Values {
	q := url.Values{}
	o.ListOptions.encode(q)
	setCSV(q, "state", o.State)
	setCSV(q, "state_not", o.StateNot)
	setCSV(q, "operation", o.Operation)
	setCSV(q, "operation_not", o.OperationNot)
	if o.OrgID != "" {
		q.Set("org_id", o.OrgID)
	}
	if !o.UpdatedAfter.IsZero() {
		q.Set("updated_after", timeParam(o.UpdatedAfter))
	}
	if !o.UpdatedBefore.IsZero() {
		q.Set("updated_before", timeParam(o.UpdatedBefore))
	}
	return q
}

type sessionList struct {
	Sessions    []Session `json:"sessions"`
	TotalCount  int       `json:"total_count"`
	Limit       int       `json:"limit"`
	Offset      int       `json:"offset"`
	Unavailable int       `json:"unavailable"`
}

// List returns one page of sessions, newest first. Sessions in a list carry no events.
func (s *SessionsService) List(ctx context.Context, o SessionListOptions) (*Page[Session], error) {
	var out sessionList
	if err := s.c.Do(ctx, http.MethodGet, computeAPI+"/sessions", o.query(), nil, &out); err != nil {
		return nil, err
	}
	return &Page[Session]{Items: nonNil(out.Sessions), TotalCount: out.TotalCount, Limit: out.Limit,
		Offset: out.Offset, Unavailable: out.Unavailable}, nil
}

// All iterates every session matching o, across pages. o's paging is ignored.
func (s *SessionsService) All(ctx context.Context, o SessionListOptions) iter.Seq2[Session, error] {
	return all(ctx, func(ctx context.Context, p ListOptions) (*Page[Session], error) {
		o.ListOptions = p
		return s.List(ctx, o)
	})
}

// GetSessionOptions shapes a session read.
type GetSessionOptions struct {
	// EventsTail is how many timeline events to include, newest first. Nil
	// takes the server's default (3); 0 includes none; a negative value, all.
	EventsTail *int `json:"events_tail,omitempty"`
}

// Get returns a session. A session that does not exist, or is not the
// caller's, is a 404 *APIError (see IsNotFound).
func (s *SessionsService) Get(ctx context.Context, id string, o GetSessionOptions) (*Session, error) {
	var out Session
	if err := s.c.Do(ctx, http.MethodGet, computeAPI+"/sessions/"+url.PathEscape(id), o.query(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o GetSessionOptions) query() url.Values {
	q := url.Values{}
	if o.EventsTail != nil {
		q.Set("events_tail", strconv.Itoa(*o.EventsTail))
	}
	return q
}

// Terminate asks the platform to end a session. Only a live session can be
// terminated; any other is a 400 invalid_state.
func (s *SessionsService) Terminate(ctx context.Context, id string) (*Accepted, error) {
	var out Accepted
	if err := s.c.Do(ctx, http.MethodDelete, computeAPI+"/sessions/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LogsOptions shapes a log read.
type LogsOptions struct {
	// Tail is the number of lines, at most 5000. Zero takes the server's
	// default of 100.
	Tail int `json:"tail,omitzero"`
	// Since is an RFC 3339 time or a duration such as "30m". Empty means 24h.
	Since string `json:"since,omitempty"`
}

// Logs returns the tail of a session's container output.
func (s *SessionsService) Logs(ctx context.Context, id string, o LogsOptions) (*SessionLogs, error) {
	var out SessionLogs
	if err := s.c.Do(ctx, http.MethodGet, computeAPI+"/sessions/"+url.PathEscape(id)+"/logs", o.query(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o LogsOptions) query() url.Values {
	q := url.Values{}
	if o.Tail > 0 {
		q.Set("tail", strconv.Itoa(o.Tail))
	}
	if o.Since != "" {
		q.Set("since", o.Since)
	}
	return q
}

// ---- volumes ----

// Create submits a volume. It is created asynchronously; see Wait.
func (v *VolumesService) Create(ctx context.Context, req CreateVolumeRequest) (*Accepted, error) {
	var out Accepted
	if err := v.c.Do(ctx, http.MethodPost, computeAPI+"/volumes", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VolumeListOptions filters a volume list. Without a state filter, deleted
// volumes are left out.
type VolumeListOptions struct {
	ListOptions
	OrgID    string        `json:"org_id,omitempty"`
	State    []VolumeState `json:"state,omitempty"`
	StateNot []VolumeState `json:"state_not,omitempty"`
	// Operation and OperationNot take provisioning, resizing or deleting.
	Operation    []string `json:"operation,omitempty"`
	OperationNot []string `json:"operation_not,omitempty"`
	// Attached, when set, keeps only attached (true) or detached (false) volumes.
	Attached      *bool     `json:"attached,omitempty"`
	UpdatedAfter  time.Time `json:"updated_after,omitzero"`
	UpdatedBefore time.Time `json:"updated_before,omitzero"`
}

func (o VolumeListOptions) query() url.Values {
	q := url.Values{}
	o.ListOptions.encode(q)
	if o.OrgID != "" {
		q.Set("org_id", o.OrgID)
	}
	setCSV(q, "state", o.State)
	setCSV(q, "state_not", o.StateNot)
	setCSV(q, "operation", o.Operation)
	setCSV(q, "operation_not", o.OperationNot)
	if o.Attached != nil {
		q.Set("attached", strconv.FormatBool(*o.Attached))
	}
	if !o.UpdatedAfter.IsZero() {
		q.Set("updated_after", timeParam(o.UpdatedAfter))
	}
	if !o.UpdatedBefore.IsZero() {
		q.Set("updated_before", timeParam(o.UpdatedBefore))
	}
	return q
}

type volumeList struct {
	Volumes     []Volume `json:"volumes"`
	TotalCount  int      `json:"total_count"`
	Limit       int      `json:"limit"`
	Offset      int      `json:"offset"`
	Unavailable int      `json:"unavailable"`
}

// List returns one page of volumes, newest first.
func (v *VolumesService) List(ctx context.Context, o VolumeListOptions) (*Page[Volume], error) {
	var out volumeList
	if err := v.c.Do(ctx, http.MethodGet, computeAPI+"/volumes", o.query(), nil, &out); err != nil {
		return nil, err
	}
	return &Page[Volume]{Items: nonNil(out.Volumes), TotalCount: out.TotalCount, Limit: out.Limit,
		Offset: out.Offset, Unavailable: out.Unavailable}, nil
}

// All iterates every volume matching o, across pages. o's paging is ignored.
func (v *VolumesService) All(ctx context.Context, o VolumeListOptions) iter.Seq2[Volume, error] {
	return all(ctx, func(ctx context.Context, p ListOptions) (*Page[Volume], error) {
		o.ListOptions = p
		return v.List(ctx, o)
	})
}

// Get returns a volume, including a deleted one.
func (v *VolumesService) Get(ctx context.Context, id string) (*Volume, error) {
	var out Volume
	if err := v.c.Do(ctx, http.MethodGet, computeAPI+"/volumes/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete asks the platform to delete a volume, detaching it first if needed.
func (v *VolumesService) Delete(ctx context.Context, id string) (*Accepted, error) {
	var out Accepted
	if err := v.c.Do(ctx, http.MethodDelete, computeAPI+"/volumes/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Extend grows a volume to sizeGB. Shrinking is not supported.
func (v *VolumesService) Extend(ctx context.Context, id string, sizeGB int) (*Accepted, error) {
	var out Accepted
	body := map[string]int{"size_gb": sizeGB}
	if err := v.c.Do(ctx, http.MethodPatch, computeAPI+"/volumes/"+url.PathEscape(id), nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- GPUs ----

// Catalog returns every GPU model the platform knows, in stock or not. It
// needs no key.
func (g *GPUsService) Catalog(ctx context.Context) ([]CatalogEntry, error) {
	var out struct {
		GPUModels []CatalogEntry `json:"gpu_models"`
	}
	if err := g.c.DoPublic(ctx, http.MethodGet, computeAPI+"/catalog", nil, &out); err != nil {
		return nil, err
	}
	return nonNil(out.GPUModels), nil
}

// AvailabilityOptions filters availability. The zero value lists models in
// stock now, summary detail, GPU machines only.
type AvailabilityOptions struct {
	Families    []string `json:"gpu_family,omitempty"`
	FormFactors []string `json:"form_factor,omitempty"`
	MinVRAMGB   int      `json:"min_vram_gb,omitzero"`
	MinGPUCount int      `json:"min_gpu_count,omitzero"`
	// Location is a location code, such as "FI-1".
	Location string `json:"location,omitempty"`
	// Deprecated: refused by the platform with a 400. Filter by Location.
	Provider string `json:"provider,omitempty"`
	// MinHourlyRate and MaxHourlyRate bound the price, EUR per hour, inclusive.
	MinHourlyRate *float64 `json:"min_hourly_rate,omitempty"`
	MaxHourlyRate *float64 `json:"max_hourly_rate,omitempty"`
	// IncludeUnavailable lists models with no stock too.
	IncludeUnavailable bool `json:"include_unavailable,omitzero"`
	// Full adds form factor, FP16 TFLOPS, price age and configurations.
	Full bool `json:"full,omitzero"`
	// IncludeCPU lists machines without GPUs too.
	IncludeCPU bool `json:"include_cpu,omitzero"`
}

func (o AvailabilityOptions) query() url.Values {
	q := url.Values{}
	setCSV(q, "gpu_family", o.Families)
	setCSV(q, "form_factor", o.FormFactors)
	if o.MinVRAMGB > 0 {
		q.Set("min_vram_gb", strconv.Itoa(o.MinVRAMGB))
	}
	if o.MinGPUCount > 0 {
		q.Set("min_gpu_count", strconv.Itoa(o.MinGPUCount))
	}
	if o.Location != "" {
		q.Set("location", o.Location)
	}
	if o.Provider != "" {
		q.Set("provider", o.Provider)
	}
	if o.MinHourlyRate != nil {
		q.Set("min_hourly_rate", strconv.FormatFloat(*o.MinHourlyRate, 'f', -1, 64))
	}
	if o.MaxHourlyRate != nil {
		q.Set("max_hourly_rate", strconv.FormatFloat(*o.MaxHourlyRate, 'f', -1, 64))
	}
	if o.IncludeUnavailable {
		q.Set("available_only", "false")
	}
	if o.Full {
		q.Set("detail", "full")
	}
	if o.IncludeCPU {
		q.Set("include_cpu", "true")
	}
	return q
}

// Availability returns GPU models with their prices and stock. It needs no key.
func (g *GPUsService) Availability(ctx context.Context, o AvailabilityOptions) ([]GPUModel, error) {
	var out struct {
		GPUModels []GPUModel `json:"gpu_models"`
	}
	if err := g.c.DoPublic(ctx, http.MethodGet, computeAPI+"/availability", o.query(), &out); err != nil {
		return nil, err
	}
	// The API answers null rather than [] when nothing matches.
	return nonNil(out.GPUModels), nil
}

// ---- account ----

// Balance returns the balance of the account the caller's sessions bill to.
func (a *AccountService) Balance(ctx context.Context) (*Balance, error) {
	return a.balance(ctx, computeAPI+"/balance")
}

// OrgBalance returns an organisation's balance, by ID or slug. Members only.
// While an organisation has no account of its own it bills the account that
// agreed to pay for it, and only that payer is shown those figures: any other
// member reads a zero balance. (requires elevated rights)
func (a *AccountService) OrgBalance(ctx context.Context, idOrSlug string) (*Balance, error) {
	return a.balance(ctx, computeAPI+"/balance/org/"+url.PathEscape(idOrSlug))
}

func (a *AccountService) balance(ctx context.Context, path string) (*Balance, error) {
	var out Balance
	if err := a.c.Do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TransactionListOptions filters transactions.
type TransactionListOptions struct {
	ListOptions
	// CreatedAfter is inclusive, CreatedBefore exclusive.
	CreatedAfter  time.Time `json:"created_after,omitzero"`
	CreatedBefore time.Time `json:"created_before,omitzero"`
	// Type is "credit" or "debit".
	Type string `json:"type,omitempty"`
	// ResourceID keeps the rows that charged one session or volume.
	ResourceID string `json:"resource_id,omitempty"`
}

func (o TransactionListOptions) query() url.Values {
	q := url.Values{}
	o.ListOptions.encode(q)
	if !o.CreatedAfter.IsZero() {
		q.Set("created_after", timeParam(o.CreatedAfter))
	}
	if !o.CreatedBefore.IsZero() {
		q.Set("created_before", timeParam(o.CreatedBefore))
	}
	if o.Type != "" {
		q.Set("type", o.Type)
	}
	if o.ResourceID != "" {
		q.Set("resource_id", o.ResourceID)
	}
	return q
}

type transactionList struct {
	Transactions []Transaction `json:"transactions"`
	TotalCount   int           `json:"total_count"`
	Limit        int           `json:"limit"`
	Offset       int           `json:"offset"`
}

// Transactions returns one page of the caller's transactions, newest first.
func (a *AccountService) Transactions(ctx context.Context, o TransactionListOptions) (*Page[Transaction], error) {
	var out transactionList
	if err := a.c.Do(ctx, http.MethodGet, computeAPI+"/transactions", o.query(), nil, &out); err != nil {
		return nil, err
	}
	return &Page[Transaction]{Items: nonNil(out.Transactions), TotalCount: out.TotalCount,
		Limit: out.Limit, Offset: out.Offset}, nil
}

// AllTransactions iterates every transaction matching o, across pages.
func (a *AccountService) AllTransactions(ctx context.Context, o TransactionListOptions) iter.Seq2[Transaction, error] {
	return all(ctx, func(ctx context.Context, p ListOptions) (*Page[Transaction], error) {
		o.ListOptions = p
		return a.Transactions(ctx, o)
	})
}

// Quota returns the caller's usage limits.
func (a *AccountService) Quota(ctx context.Context) (*Quota, error) {
	var out Quota
	if err := a.c.Do(ctx, http.MethodGet, computeAPI+"/quota", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- helpers ----

func setCSV[T ~string](q url.Values, key string, vals []T) {
	if len(vals) == 0 {
		return
	}
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		if s := strings.TrimSpace(string(v)); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) > 0 {
		q.Set(key, strings.Join(parts, ","))
	}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
