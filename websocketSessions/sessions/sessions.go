package sessions

import (
	"context"
	"errors"
	"github.com/gorilla/websocket"
	"github.com/reeceappling/goUtils/v2/utils"
	"github.com/reeceappling/pi-pn532-i2c-Ntag21x-ws/v2/websocketSessions/shared"
	"sync"
	"time"
)

// TODO: everything below here is new

type Session struct { // TODO: reevaluate
	Conn             *websocket.Conn
	Expires          time.Time
	maxCheckFailures int
	requestTimeout   time.Duration
	Close            context.CancelFunc
	mutex            *sync.Mutex
	Managed          bool
	ttl              time.Duration
	ExpiryTimer      *time.Timer
	failedChecks     int
}

func New(cancelFunc context.CancelFunc, conn *websocket.Conn, sessionTTL *time.Duration, reqTimeout *time.Duration, timeBtwnChecks *time.Duration, maxCheckFailures *int) *Session { // TODO: before destroying session, lock
	sessionTimeout := utils.Default(sessionTTL, 5*time.Minute)
	timeout := utils.Default(reqTimeout, 30*time.Second)
	return &Session{
		Conn:             conn,
		ttl:              sessionTimeout,
		maxCheckFailures: utils.Default(maxCheckFailures, 0),
		Expires:          time.Now().Add(timeout), // TODO: ensure this is ok
		requestTimeout:   timeout,
		mutex:            &sync.Mutex{},
		Close:            cancelFunc,
		Managed:          false,
		ExpiryTimer:      time.NewTimer(sessionTimeout),
		failedChecks:     0,
	}
}

// Does nothing if a session has not been added to a SessionManager or has been closed
func (sess *Session) End() { // TODO: reevaluate
	if sess.Managed {
		sess.Close()
	}
}

func (s *Session) processSuccessfulRenewal() {
	s.SetSessionExpiration(time.Now().Add(s.ttl)) // TODO: may now be unnecessary
	s.failedChecks = 0
	s.ExpiryTimer.Reset(s.ttl)
}
func (s *Session) processRenewalFailure() {
	s.failedChecks++
	if s.failedChecks > s.maxCheckFailures { // TODO: ok?
		s.Close()
		return
	}
	s.SetSessionExpiration(time.Now().Add(s.ttl)) // TODO: may now be unnecessary
	s.ExpiryTimer.Reset(s.ttl)
}

func (s *Session) TryRenew(name, expSecret string) (renewErr error) {
	s.mutex.Lock()
	s.ExpiryTimer.Stop()
	defer s.mutex.Unlock()

	err := shared.NewRenewalRequest(name).WriteTo(s.Conn) // TODO: pong handler?
	if err != nil {
		s.processRenewalFailure()
		return err
	}
	// READ for a ping message (within the allowed timeframe)
	err = s.TryGetMessage().ValidateRenewalResponse(expSecret)
	if err != nil {
		s.processRenewalFailure()
		return errors.Join(errors.New("failed to renew client lease"), err)
	}
	s.processSuccessfulRenewal()
	return nil
}

func (s *Session) SetSessionExpiration(t time.Time) {
	s.Expires = t
}

func (sess *Session) TryGetMessage() shared.ReceivedMsg {
	timedCtx, cancel := context.WithTimeout(context.Background(), sess.requestTimeout)
	defer cancel()
	return shared.TryGetMessage(timedCtx, sess.Conn)
}

func (sess *Session) TryReadRFID() ([shared.RfidByteSize]byte, error) {
	sess.mutex.Lock()
	defer sess.mutex.Unlock()
	out := [shared.RfidByteSize]byte{}
	err := shared.NewReadRequest().WriteTo(sess.Conn)
	if err != nil {
		return out, err
	}
	return sess.TryGetMessage().ProcessReadResponse()
}

func (sess *Session) TryWriteRFID(toWrite [shared.RfidByteSize]byte) error { // TODO: this is client side
	sess.mutex.Lock()
	defer sess.mutex.Unlock()
	err := shared.NewWriteRequest(toWrite).WriteTo(sess.Conn)
	if err != nil {
		return err
	}
	return sess.TryGetMessage().ValidateWriteResponse(toWrite)
}
