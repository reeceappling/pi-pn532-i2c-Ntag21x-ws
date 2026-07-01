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
	Close            func()
	*sync.Mutex
	ttl          time.Duration
	failedChecks int
}

const defaultRequestTimeout = 30 * time.Second
const defaultSessionTimeout = 5 * time.Minute
const defaultMaxSessionCheckFailures = 1

func New(conn *websocket.Conn, sessionTTL *time.Duration, reqTimeout *time.Duration, timeBtwnChecks *time.Duration, maxCheckFailures *int) *Session { // TODO: before destroying session, lock
	sessionTimeout := utils.Default(sessionTTL, defaultSessionTimeout)
	timeout := utils.Default(reqTimeout, defaultRequestTimeout)
	maxRefreshFails := utils.Default(maxCheckFailures, defaultMaxSessionCheckFailures)
	return &Session{
		Conn:             conn,
		ttl:              sessionTimeout,
		maxCheckFailures: maxRefreshFails,
		Expires:          time.Now().Add(timeout),
		requestTimeout:   timeout,
		Mutex:            &sync.Mutex{},
		Close: func() {
			err := conn.Close()
			if err != nil {
				println("Error closing session before adding to sessions: " + err.Error())
			}
		}, // TODO: ????
		failedChecks: 0,
	}
}

//// Does nothing if a session has not been added to a SessionManager or has been closed
//func (sess *Session) End() {
//	sess.Close()
//}

func (s *Session) processSuccessfulRenewal() {
	s.failedChecks = 0
	s.SetSessionExpiration(time.Now().Add(s.ttl))
}
func (s *Session) processRenewalFailure() {
	s.failedChecks++
	if s.failedChecks >= s.maxCheckFailures {
		s.Close()
		return
	}
	s.SetSessionExpiration(time.Now().Add(s.ttl))
}

func (s *Session) TryRenew(name, expSecret string) (renewErr error) {
	s.Lock()
	defer s.Unlock()

	err := shared.NewRenewalRequest(name).WriteTo(s.Conn) // TODO: pong handler?
	if err != nil {
		s.processRenewalFailure()
		return errors.Join(errors.New("failed to send renewal message"), err)
	}
	// READ for a pong message (within the allowed timeframe)
	msg := s.TryGetMessage(context.Background(), 5*time.Second)
	if msg.Err != nil {
		s.processRenewalFailure()
		return errors.New("bad renewal response") // TODO: ok?
	}
	if msg.MsgType != websocket.PongMessage {
		s.processRenewalFailure()
		return errors.New("bad renewal response type") // TODO: ok?
	}
	sm := msg.SocketMsgUnsafe()
	if len(sm.Data) == 0 {
		s.processRenewalFailure()
		return errors.New("response too small") // TODO: ok?
	}
	if string(sm.Data) != expSecret {
		s.processRenewalFailure()
		return errors.Join(errors.New("failed to renew client lease, secret mismatch"), err)
	}
	s.processSuccessfulRenewal()
	return nil
}

func (s *Session) SetSessionExpiration(t time.Time) {
	s.Expires = t
}

func (sess *Session) TryGetMessage(ctx context.Context, timeout ...time.Duration) shared.ReceivedMsg {
	timedCtx, cancel := context.WithTimeout(ctx, sess.requestTimeout)
	defer cancel()
	return shared.TryGetMessage(timedCtx, sess.Conn, timeout...) // TODO: time ok?
}

const readResponseTimeout = 20 * time.Second  // TODO: time ok?
const writeResponseTimeout = 20 * time.Second // TODO: time ok?

func (sess *Session) TryReadRFID(ctx context.Context) ([shared.RfidByteSize]byte, error) {
	sess.Lock()
	defer sess.Unlock()
	println("creating read request") // TODO: del
	err := shared.NewReadRequest().
		WriteTo(sess.Conn)
	if err != nil {
		println("failed to write read request: " + err.Error()) // TODO: del
		return [shared.RfidByteSize]byte{}, err
	}
	println("trying to get response") // TODO: del
	return sess.TryGetMessage(ctx, readResponseTimeout).
		ProcessReadResponse()
}

func (sess *Session) TryWriteRFID(ctx context.Context, toWrite [shared.RfidByteSize]byte) error { // TODO: this is serverside
	sess.Lock()
	defer sess.Unlock()
	err := shared.NewWriteRequest(toWrite).
		WriteTo(sess.Conn)
	if err != nil {
		return err
	}
	return sess.TryGetMessage(ctx, writeResponseTimeout).
		ProcessWriteResponse(toWrite)
}
