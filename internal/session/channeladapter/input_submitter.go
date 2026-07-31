package channeladapter

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	defaultPerRuneDelay = 5 * time.Millisecond
	defaultSubmitDelay  = 30 * time.Millisecond
)

// InputSubmitter mirrors xterm's one-write-per-keystroke PTY behavior. The
// final carriage return is always a separate write after the settle delay.
type InputSubmitter struct {
	input        Inputter
	perRuneDelay time.Duration
	submitDelay  time.Duration
}

func NewInputSubmitter(input Inputter) *InputSubmitter {
	return &InputSubmitter{input: input, perRuneDelay: defaultPerRuneDelay, submitDelay: defaultSubmitDelay}
}

func newInputSubmitterForTest(input Inputter, perRuneDelay, submitDelay time.Duration) *InputSubmitter {
	return &InputSubmitter{input: input, perRuneDelay: perRuneDelay, submitDelay: submitDelay}
}

func (s *InputSubmitter) Submit(ctx context.Context, sessionID, text string) error {
	if s == nil || s.input == nil {
		return errors.New("session input not configured")
	}
	for _, r := range text {
		if err := s.input.Input(ctx, sessionID, []byte(string(r))); err != nil {
			return err
		}
		if err := wait(ctx, s.perRuneDelay); err != nil {
			return err
		}
	}
	if err := wait(ctx, s.submitDelay); err != nil {
		return err
	}
	if err := s.input.Input(ctx, sessionID, []byte{'\r'}); err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	return nil
}

func wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
