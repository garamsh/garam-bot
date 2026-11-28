package modelgateway

import (
	"io"
	"time"
)

// stalling is an answer's body that calls stalled once it has passed no byte for idle, and never after
// it is closed. The gateway breaks the call off there, so an upstream that stops sending without
// closing holds no call for longer than idle (MOD-6 answers the break).
type stalling struct {
	body  io.ReadCloser
	idle  time.Duration
	timer *time.Timer
}

func newStalling(body io.ReadCloser, idle time.Duration, stalled func()) *stalling {
	return &stalling{body: body, idle: idle, timer: time.AfterFunc(idle, stalled)}
}

func (s *stalling) Read(p []byte) (int, error) {
	n, err := s.body.Read(p)
	if n > 0 {
		s.timer.Reset(s.idle)
	}
	return n, err
}

func (s *stalling) Close() error {
	s.timer.Stop()
	return s.body.Close()
}
