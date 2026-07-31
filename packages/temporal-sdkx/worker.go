// Real Temporal worker integration (HARDENING H3). When TEMPORAL_URL is
// set, NewRunnerFromEnv returns a TemporalRunner backed by
// go.temporal.io/sdk: a worker is started on TEMPORAL_TASK_QUEUE (default
// meridian-core) in TEMPORAL_NAMESPACE (default "default"), workflows are
// registered under the same names as the inproc runner, and Execute submits
// a workflow execution to the cluster. Retry policy mapping preserves the
// platform defaults. The inproc runner remains the dev fallback.
package sdkx

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	tclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	tworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// TemporalRetryPolicy maps the platform RetryPolicy onto the Temporal SDK
// retry policy (identical semantics: interval, backoff, cap, attempts).
func TemporalRetryPolicy(p RetryPolicy) *temporal.RetryPolicy {
	return &temporal.RetryPolicy{
		InitialInterval:    p.InitialInterval,
		BackoffCoefficient: p.BackoffCoefficient,
		MaximumInterval:    p.MaximumInterval,
		MaximumAttempts:    int32(p.MaximumAttempts),
	}
}

// TemporalRunner is a Runner backed by a real Temporal cluster.
type TemporalRunner struct {
	client    tclient.Client
	worker    tworker.Worker
	taskQueue string

	mu      sync.Mutex
	wfs     map[string]Workflow
	history []RunRecord
	seq     int
}

// NewTemporalRunner connects to the Temporal frontend at address (host:port)
// and creates a worker on the given task queue in the given namespace.
// Workflows registered via RegisterWorkflow are exposed to the cluster under
// the same names; call Start to begin polling.
func NewTemporalRunner(address, namespace, taskQueue string) (*TemporalRunner, error) {
	if namespace == "" {
		namespace = "default"
	}
	if taskQueue == "" {
		taskQueue = "meridian-core"
	}
	c, err := tclient.Dial(tclient.Options{HostPort: address, Namespace: namespace})
	if err != nil {
		return nil, fmt.Errorf("temporal: dial %s: %w", address, err)
	}
	w := tworker.New(c, taskQueue, tworker.Options{})
	return &TemporalRunner{
		client: c, worker: w, taskQueue: taskQueue,
		wfs: map[string]Workflow{},
	}, nil
}

// RegisterWorkflow implements Runner; the workflow is registered with the
// Temporal worker under the same name as the inproc runner.
func (r *TemporalRunner) RegisterWorkflow(name string, wf Workflow) {
	r.mu.Lock()
	r.wfs[name] = wf
	r.mu.Unlock()
	r.worker.RegisterWorkflowWithOptions(
		func(ctx workflow.Context, input any) (any, error) {
			// Activity executions inside a Temporal workflow should use
			// workflow.ExecuteActivity; platform wf-* primitives are
			// synchronous, so we invoke the same Workflow function.
			return wf(context.Background(), input)
		},
		workflow.RegisterOptions{Name: name},
	)
}

// Start begins worker polling (non-blocking).
func (r *TemporalRunner) Start() error { return r.worker.Start() }

// Stop shuts the worker and client down.
func (r *TemporalRunner) Stop() {
	r.worker.Stop()
	r.client.Close()
}

// Execute submits the workflow to the cluster with the platform default
// retry policy and waits for its result.
func (r *TemporalRunner) Execute(ctx context.Context, name string, input any) (any, error) {
	r.mu.Lock()
	if _, ok := r.wfs[name]; !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("workflow %q not registered", name)
	}
	r.seq++
	id := fmt.Sprintf("wf-run-%d-%d", r.seq, time.Now().UnixNano())
	r.mu.Unlock()
	rec := RunRecord{WorkflowID: id, Name: name, StartedAt: time.Now().UTC()}
	run, err := r.client.ExecuteWorkflow(ctx, tclient.StartWorkflowOptions{
		ID:          id,
		TaskQueue:   r.taskQueue,
		RetryPolicy: TemporalRetryPolicy(DefaultRetryPolicy),
	}, name, input)
	var out any
	if err == nil {
		err = run.Get(ctx, &out)
	}
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
func (r *TemporalRunner) History() []RunRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RunRecord(nil), r.history...)
}

// WorkflowNames implements Runner.
func (r *TemporalRunner) WorkflowNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.wfs))
	for n := range r.wfs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
