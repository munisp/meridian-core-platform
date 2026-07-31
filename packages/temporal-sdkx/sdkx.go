// Package sdkx provides Temporal-style workflow helpers (SPEC 2): retry
// policy, SAGA compensation, an activity registry and a dev in-process
// runner (used when TEMPORAL_URL is unset). The Runner interface mirrors
// the subset of Temporal semantics planes rely on; a real Temporal worker
// can be wired behind the same interface when the SDK is linked.
package sdkx

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"sync"
	"time"
)

// RetryPolicy mirrors Temporal's activity retry policy.
type RetryPolicy struct {
	InitialInterval    time.Duration
	BackoffCoefficient float64
	MaximumInterval    time.Duration
	MaximumAttempts    int
}

// DefaultRetryPolicy is the platform default (SPEC: retry policy helper).
var DefaultRetryPolicy = RetryPolicy{
	InitialInterval:    500 * time.Millisecond,
	BackoffCoefficient: 2.0,
	MaximumInterval:    30 * time.Second,
	MaximumAttempts:    5,
}

func (p RetryPolicy) delay(attempt int) time.Duration {
	d := float64(p.InitialInterval) * math.Pow(p.BackoffCoefficient, float64(attempt-1))
	if p.MaximumInterval > 0 && time.Duration(d) > p.MaximumInterval {
		return p.MaximumInterval
	}
	return time.Duration(d)
}

// Activity is a unit of work with a name (for the registry + audit).
type Activity func(ctx context.Context, input any) (any, error)

// ActivityRegistry maps activity names to implementations.
type ActivityRegistry struct {
	mu   sync.RWMutex
	acts map[string]Activity
}

// NewActivityRegistry creates an empty registry.
func NewActivityRegistry() *ActivityRegistry {
	return &ActivityRegistry{acts: map[string]Activity{}}
}

// Register adds an activity (panics on duplicate — programmer error).
func (r *ActivityRegistry) Register(name string, a Activity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.acts[name]; exists {
		panic(fmt.Sprintf("activity %q already registered", name))
	}
	r.acts[name] = a
}

// Get fetches an activity.
func (r *ActivityRegistry) Get(name string) (Activity, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.acts[name]
	return a, ok
}

// Names lists registered activities (for the admin console catalog).
func (r *ActivityRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.acts))
	for n := range r.acts {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ExecuteActivity runs a named activity with retry per policy.
func ExecuteActivity(ctx context.Context, reg *ActivityRegistry, policy RetryPolicy, name string, input any) (any, error) {
	act, ok := reg.Get(name)
	if !ok {
		return nil, fmt.Errorf("activity %q not registered", name)
	}
	if policy.MaximumAttempts <= 0 {
		policy.MaximumAttempts = 1
	}
	var err error
	var out any
	for attempt := 1; attempt <= policy.MaximumAttempts; attempt++ {
		out, err = act(ctx, input)
		if err == nil {
			return out, nil
		}
		if attempt < policy.MaximumAttempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(policy.delay(attempt)):
			}
		}
	}
	return nil, fmt.Errorf("activity %q failed after %d attempts: %w", name, policy.MaximumAttempts, err)
}

// Compensation undoes a completed saga step.
type Compensation func(ctx context.Context, output any) error

// SagaStep couples an action with its compensation.
type SagaStep struct {
	Name         string
	Action       Activity
	Compensation Compensation // nil = no compensation needed
}

// Saga executes steps in order, compensating in reverse on failure
// (SPEC: SAGA helpers).
type Saga struct {
	Steps []SagaStep
	log   func(format string, args ...any)
}

// NewSaga creates a saga with a log function (nil = stdlib log).
func NewSaga(steps ...SagaStep) *Saga {
	return &Saga{Steps: steps, log: log.Printf}
}

// Run executes the saga; on step failure, runs compensations for completed
// steps in reverse order and returns a SagaError.
func (s *Saga) Run(ctx context.Context, input any) error {
	type done struct {
		step   SagaStep
		output any
	}
	var completed []done
	for i, step := range s.Steps {
		out, err := step.Action(ctx, input)
		if err != nil {
			s.log("saga: step %d (%s) failed: %v; compensating %d steps", i, step.Name, err, len(completed))
			var compErrs []error
			for j := len(completed) - 1; j >= 0; j-- {
				if completed[j].step.Compensation == nil {
					continue
				}
				if cerr := completed[j].step.Compensation(ctx, completed[j].output); cerr != nil {
					compErrs = append(compErrs, fmt.Errorf("compensate %s: %w", completed[j].step.Name, cerr))
				}
			}
			return &SagaError{Step: step.Name, Err: err, CompensationErrors: compErrs}
		}
		completed = append(completed, done{step, out})
	}
	return nil
}

// SagaError reports the failed step and any compensation failures.
type SagaError struct {
	Step               string
	Err                error
	CompensationErrors []error
}

func (e *SagaError) Error() string {
	return fmt.Sprintf("saga failed at step %q: %v (%d compensation errors)", e.Step, e.Err, len(e.CompensationErrors))
}

func (e *SagaError) Unwrap() error { return e.Err }

// Workflow is a named workflow function.
type Workflow func(ctx context.Context, input any) (any, error)

// RunRecord captures one workflow execution (history for the admin console).
type RunRecord struct {
	WorkflowID string        `json:"workflow_id"`
	Name       string        `json:"name"`
	StartedAt  time.Time     `json:"started_at"`
	EndedAt    time.Time     `json:"ended_at"`
	Status     string        `json:"status"` // completed|failed
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration_ns"`
}

// Runner executes workflows (in-process in dev; Temporal worker in prod).
type Runner interface {
	RegisterWorkflow(name string, wf Workflow)
	Execute(ctx context.Context, name string, input any) (any, error)
	History() []RunRecord
	WorkflowNames() []string
}

// InprocRunner is the dev in-process runner (TEMPORAL_URL unset).
type InprocRunner struct {
	mu      sync.Mutex
	wfs     map[string]Workflow
	history []RunRecord
	seq     int
}

// NewInprocRunner creates the dev runner.
func NewInprocRunner() *InprocRunner {
	return &InprocRunner{wfs: map[string]Workflow{}}
}

// RegisterWorkflow implements Runner.
func (r *InprocRunner) RegisterWorkflow(name string, wf Workflow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wfs[name] = wf
}

// Execute runs the workflow synchronously and records history.
func (r *InprocRunner) Execute(ctx context.Context, name string, input any) (any, error) {
	r.mu.Lock()
	wf, ok := r.wfs[name]
	r.seq++
	id := fmt.Sprintf("wf-run-%d", r.seq)
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("workflow %q not registered", name)
	}
	rec := RunRecord{WorkflowID: id, Name: name, StartedAt: time.Now().UTC()}
	out, err := wf(ctx, input)
	rec.EndedAt = time.Now().UTC()
	rec.Duration = rec.EndedAt.Sub(rec.StartedAt)
	if err != nil {
		rec.Status = "failed"
		rec.Error = err.Error()
	} else {
		rec.Status = "completed"
	}
	r.mu.Lock()
	r.history = append(r.history, rec)
	r.mu.Unlock()
	return out, err
}

// History implements Runner.
func (r *InprocRunner) History() []RunRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RunRecord(nil), r.history...)
}

// WorkflowNames implements Runner.
func (r *InprocRunner) WorkflowNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.wfs))
	for n := range r.wfs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// NewRunnerFromEnv returns the runner per HARDENING H1: TEMPORAL_URL set ->
// real Temporal worker (profile=prod); otherwise the in-process dev runner.
// Startup never fails because Temporal is unreachable — it falls back to
// the inproc runner with a dev-profile log line.
func NewRunnerFromEnv() Runner {
	if url := os.Getenv("TEMPORAL_URL"); url != "" {
		ns := os.Getenv("TEMPORAL_NAMESPACE")
		tq := os.Getenv("TEMPORAL_TASK_QUEUE")
		tr, err := NewTemporalRunner(url, ns, tq)
		if err != nil {
			log.Printf("profile=dev component=temporal worker init failed (%v); inproc fallback", err)
			return NewInprocRunner()
		}
		if err := tr.Start(); err != nil {
			log.Printf("profile=dev component=temporal worker start failed (%v); inproc fallback", err)
			tr.Stop()
			return NewInprocRunner()
		}
		log.Printf("profile=prod component=temporal url=%s namespace=%q task_queue=%q", url, ns, tq)
		return tr
	}
	log.Printf("profile=dev component=temporal inproc")
	return NewInprocRunner()
}
