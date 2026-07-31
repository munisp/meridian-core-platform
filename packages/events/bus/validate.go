package bus

import (
	"context"
	"log"
	"os"
	"sync"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
)

// PublishValidator validates an envelope before it is published to a topic
// (audit I3). Typically backed by schemareg.Registry.ValidateEnvelope.
type PublishValidator func(topic string, env envelope.Envelope) error

var (
	validatorMu      sync.RWMutex
	publishValidator PublishValidator
)

// SetPublishValidator installs the ValidateBeforePublish hook. All buses
// built by NewFromEnv consult it. Pass nil to disable.
func SetPublishValidator(v PublishValidator) {
	validatorMu.Lock()
	defer validatorMu.Unlock()
	publishValidator = v
}

// ValidateBeforePublish runs the installed hook. Mode (PROFILE env):
//   - prod: unregistered/invalid events are REJECTED (error returned)
//   - dev (default): problems are logged as warnings and publish proceeds
//
// Returns nil when publishing may proceed.
func ValidateBeforePublish(topic string, env envelope.Envelope) error {
	validatorMu.RLock()
	v := publishValidator
	validatorMu.RUnlock()
	if v == nil {
		return nil
	}
	err := v(topic, env)
	if err == nil {
		return nil
	}
	if os.Getenv("PROFILE") == "prod" {
		return err
	}
	log.Printf("profile=dev component=schemareg WARN publish validation topic=%s: %v", topic, err)
	return nil
}

// validatingBus wraps a Bus with the ValidateBeforePublish hook.
type validatingBus struct{ inner Bus }

// NewValidating wraps b so every Publish runs ValidateBeforePublish first.
func NewValidating(b Bus) Bus { return &validatingBus{inner: b} }

func (w *validatingBus) Publish(ctx context.Context, topic string, env envelope.Envelope) error {
	if err := ValidateBeforePublish(topic, env); err != nil {
		return err
	}
	return w.inner.Publish(ctx, topic, env)
}

func (w *validatingBus) Subscribe(topic string, h Handler) (func(), error) {
	return w.inner.Subscribe(topic, h)
}

func (w *validatingBus) Close() error { return w.inner.Close() }
