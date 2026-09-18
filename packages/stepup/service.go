package stepup

import (
	"errors"
	"time"
)

// Service bundles the TOTP enrollment lifecycle for one service.
type Service struct {
	Store  Store
	Sealer *Sealer
	Issuer string // otpauth issuer label
	now    func() time.Time
}

// NewService wires a Service. profile is PROFILE ("dev"/"prod").
func NewService(store Store, profile, issuer string) (*Service, error) {
	sealer, err := NewSealer(profile)
	if err != nil {
		return nil, err
	}
	if issuer == "" {
		issuer = "Meridian"
	}
	return &Service{Store: store, Sealer: sealer, Issuer: issuer, now: time.Now}, nil
}

// ErrNotConfirmed: enrollment exists but confirm step not completed.
var ErrNotConfirmed = errors.New("stepup: enrollment not confirmed")

// Enroll starts (or restarts) enrollment for actor. Returns the plaintext
// secret, otpauth URI and recovery codes — shown once, never stored raw.
// The record stays disabled until Confirm.
func (s *Service) Enroll(actor string) (secret, uri string, recovery []string, err error) {
	secret, err = GenerateSecret()
	if err != nil {
		return "", "", nil, err
	}
	sealed, err := s.Sealer.Seal(secret)
	if err != nil {
		return "", "", nil, err
	}
	codes, hashes, err := GenerateRecoveryCodes()
	if err != nil {
		return "", "", nil, err
	}
	rec := &Record{
		Actor:          actor,
		SealedSecret:   sealed,
		RecoveryHashes: hashes,
		Enabled:        false,
		CreatedAt:      s.now(),
	}
	if err := s.Store.Put(rec); err != nil {
		return "", "", nil, err
	}
	return secret, OTPAuthURI(s.Issuer, actor, secret), codes, nil
}

// Confirm activates enrollment after the first valid TOTP code.
func (s *Service) Confirm(actor, code string) error {
	rec, err := s.Store.Get(actor)
	if err != nil {
		return err
	}
	secret, err := s.Sealer.Open(rec.SealedSecret)
	if err != nil {
		return err
	}
	if !Validate(secret, code, s.now()) {
		return ErrBadCode
	}
	rec.Enabled = true
	rec.ConfirmedAt = s.now()
	return s.Store.Put(rec)
}

// Disable removes enrollment. Callers MUST require a successful step-up
// (VerifyCode / ticket) before calling Disable.
func (s *Service) Disable(actor string) error { return s.Store.Delete(actor) }

// ErrBadCode: TOTP/recovery code did not validate.
var ErrBadCode = errors.New("stepup: invalid code")

// Enrolled reports whether the actor has an active enrollment.
func (s *Service) Enrolled(actor string) bool {
	rec, err := s.Store.Get(actor)
	return err == nil && rec.Enabled
}

// VerifyCode checks a TOTP code OR a single-use recovery code for an
// enrolled actor. A used recovery code is burned immediately.
func (s *Service) VerifyCode(actor, code string) error {
	rec, err := s.Store.Get(actor)
	if err != nil {
		return err
	}
	if !rec.Enabled {
		return ErrNotConfirmed
	}
	secret, err := s.Sealer.Open(rec.SealedSecret)
	if err != nil {
		return err
	}
	if Validate(secret, code, s.now()) {
		return nil
	}
	// recovery path: hash match => burn and accept
	h := HashRecoveryCode(code)
	for i, rh := range rec.RecoveryHashes {
		if rh == h {
			rec.RecoveryHashes = append(rec.RecoveryHashes[:i], rec.RecoveryHashes[i+1:]...)
			if err := s.Store.Put(rec); err != nil {
				return err
			}
			return nil
		}
	}
	return ErrBadCode
}
