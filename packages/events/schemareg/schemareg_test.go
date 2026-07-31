package schemareg

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
)

func TestDevRegistryLoadsCatalog(t *testing.T) {
	r, err := NewDev()
	if err != nil {
		t.Fatalf("NewDev: %v", err)
	}
	topics := r.Topics()
	if len(topics) < 30 {
		t.Fatalf("expected >=30 catalogued topics, got %d", len(topics))
	}
	for _, want := range []string{
		"nrs.psm.payments.v1", "nrs.ledger.transfers.v1", "nrs.onb.ussd.v1",
		"nrs.ml.scored.v1", "nrs.feature.materialised.v1", "nrs.mbs.preclearance.v1",
	} {
		if _, ok := r.Lookup(want); !ok {
			t.Errorf("topic %s not registered in dev store", want)
		}
	}
}

func TestValidateDataOK(t *testing.T) {
	r, _ := NewDev()
	payload := `{"reference":"r1","amount_kobo":12500,"tin_hash":"` + strings.Repeat("a", 64) + `"}`
	if err := r.ValidateData("nrs.psm.payments.v1", json.RawMessage(payload)); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
}

func TestValidateDataRejectsBadType(t *testing.T) {
	r, _ := NewDev()
	payload := `{"reference":"r1","amount_kobo":"not-an-int"}`
	err := r.ValidateData("nrs.psm.payments.v1", json.RawMessage(payload))
	if err == nil {
		t.Fatal("expected validation error for string amount_kobo")
	}
	var inv *ErrInvalid
	if !errors.As(err, &inv) {
		t.Fatalf("expected ErrInvalid, got %T", err)
	}
}

func TestValidateDataRejectsMissingRequired(t *testing.T) {
	r, _ := NewDev()
	err := r.ValidateData("nrs.ml.scored.v1", json.RawMessage(`{"entity_hash":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("expected missing-required error, got %v", err)
	}
}

func TestValidateDataUnregistered(t *testing.T) {
	r, _ := NewDev()
	err := r.ValidateData("nrs.nope.nothing.v1", json.RawMessage(`{}`))
	var unreg *ErrUnregistered
	if !errors.As(err, &unreg) {
		t.Fatalf("expected ErrUnregistered, got %v", err)
	}
}

func TestValidateEnvelopeShape(t *testing.T) {
	r, _ := NewDev()
	env, err := envelope.New("nrs.psm.payments.v1", "svc-test", "", "", map[string]any{
		"reference": "r1", "amount_kobo": 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateEnvelope(env); err != nil {
		t.Fatalf("canonical envelope rejected: %v", err)
	}
	env.Type = "bogus.topic"
	if err := r.ValidateEnvelope(env); err == nil {
		t.Fatal("expected shape error for bad type")
	}
}

func TestCheckCompatibility(t *testing.T) {
	r := New()
	v1 := `{"type":"object","required":["a"],"properties":{"a":{"type":"string"},"b":{"type":"integer","enum":["x","y"]}}}`
	if err := r.Register("nrs.test.compat.v1", json.RawMessage(v1)); err != nil {
		t.Fatal(err)
	}
	// backward compatible: adds optional field
	ok := `{"type":"object","required":["a"],"properties":{"a":{"type":"string"},"b":{"type":"integer","enum":["x","y","z"]},"c":{"type":"string"}}}`
	if err := r.CheckCompatibility("nrs.test.compat.v1", json.RawMessage(ok)); err != nil {
		t.Fatalf("compatible change rejected: %v", err)
	}
	// incompatible: new required field
	bad := `{"type":"object","required":["a","c"],"properties":{"a":{"type":"string"},"c":{"type":"string"}}}`
	if err := r.CheckCompatibility("nrs.test.compat.v1", json.RawMessage(bad)); err == nil {
		t.Fatal("new required field must be incompatible")
	}
	// incompatible: type change
	bad2 := `{"type":"object","required":["a"],"properties":{"a":{"type":"integer"}}}`
	if err := r.CheckCompatibility("nrs.test.compat.v1", json.RawMessage(bad2)); err == nil {
		t.Fatal("type change must be incompatible")
	}
	// incompatible: enum narrowed
	bad3 := `{"type":"object","required":["a"],"properties":{"a":{"type":"string"},"b":{"type":"integer","enum":["x"]}}}`
	if err := r.CheckCompatibility("nrs.test.compat.v1", json.RawMessage(bad3)); err == nil {
		t.Fatal("enum narrowing must be incompatible")
	}
	// unregistered topic: trivially compatible
	if err := r.CheckCompatibility("nrs.test.other.v1", json.RawMessage(bad)); err != nil {
		t.Fatalf("unregistered topic must be compatible: %v", err)
	}
}

func TestRegisterRejectsBadTopic(t *testing.T) {
	r := New()
	if err := r.Register("not-a-topic", json.RawMessage(`{}`)); err == nil {
		t.Fatal("invalid topic name must be rejected")
	}
}
