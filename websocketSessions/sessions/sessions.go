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
const cloudflareTunnelWebsocketTimeout = 100 * time.Second
const reasonableCloudflareTunnelWebsocketKeepalive = 90 * time.Second

func New(conn *websocket.Conn, sessionTTL *time.Duration, reqTimeout *time.Duration, timeBtwnChecks *time.Duration, maxCheckFailures *int) *Session { // TODO: before destroying session, lock
	sessionTimeout := utils.Default(sessionTTL, defaultSessionTimeout)
	timeout := utils.Default(reqTimeout, defaultRequestTimeout)
	maxRefreshFails := utils.Default(maxCheckFailures, defaultMaxSessionCheckFailures)
	mut := &sync.Mutex{}
	conn.SetPongHandler(func(appData string) error { // TODO: ENSURE OK!
		return nil
	})
	return &Session{
		Conn:             conn,
		ttl:              sessionTimeout,
		maxCheckFailures: maxRefreshFails,
		Expires:          time.Now().Add(timeout),
		requestTimeout:   timeout,
		Mutex:            mut,
		Close: func() {
			err := conn.Close()
			if err != nil {
				println("Error closing session before adding to sessions: " + err.Error())
			}
		},
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

var (
	ErrBadRenewResponse     = errors.New("bad renewal response")
	ErrSendRenewFail        = errors.New("failed to send renewal message")
	ErrBadRenewResponseType = errors.New("bad renewal response type")
	ErrResponseTooSmall     = errors.New("response too small")
	ErrSecretMismatch       = errors.New("secret mismatch")
)

func (s *Session) TryRenew(name, expSecret string) (renewErr error) {
	s.Lock()
	defer s.Unlock()

	s.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := shared.NewRenewalRequest(name).WriteTo(s.Conn) // TODO: pong handler?
	if err != nil {
		s.processRenewalFailure()
		return errors.Join(ErrSendRenewFail, err)
	}
	// READ for a pong message (within the allowed timeframe)
	msg := s.TryGetMessage(context.Background(), 12*time.Second)
	if msg.Err != nil {
		s.processRenewalFailure()
		return errors.Join(ErrBadRenewResponse, msg.Err)
	}
	if msg.MsgType != websocket.PongMessage {
		s.processRenewalFailure()
		return ErrBadRenewResponseType
	}
	sm := msg.SocketMsgUnsafe()
	if len(sm.Data) == 0 {
		s.processRenewalFailure()
		return ErrResponseTooSmall
	}
	if string(sm.Data) != expSecret {
		s.processRenewalFailure()
		return errors.Join(ErrSecretMismatch, err)
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
	m := shared.TryGetMessage(timedCtx, sess.Conn, timeout...)
	//println("Got message:" + m.String())                       // TODO: del?
	//if m.Err != nil {
	//	println("g0t erroneous message. Returning it anyways!")
	//}
	return m
}

const writeTimeout = 5 * time.Second
const responseTimeout = 10 * time.Second
const readResponseTimeout = responseTimeout
const writeResponseTimeout = responseTimeout

func (sess *Session) TryReadRFID(ctx context.Context) ([shared.RfidByteSize]byte, error) {
	sess.Lock()
	defer sess.Unlock()
	//println("creating read request") // TODO: del
	// TODO: write deadline ok?
	err := sess.Conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err != nil {
		println("failed to set write deadline")
		return [shared.RfidByteSize]byte{}, err
	}
	err = shared.NewReadRequest().
		WriteTo(sess.Conn)
	if err != nil {
		println("failed to write read request: " + err.Error())
		return [shared.RfidByteSize]byte{}, err
	}
	//println("trying to get response") // TODO: del
	result, err := sess.TryGetMessage(ctx, readResponseTimeout).ProcessReadResponse()
	if err != nil {
		println("error processing read response: " + err.Error())
	}
	return result, err
}

// TryWriteRFID is serverside
func (sess *Session) TryWriteRFID(ctx context.Context, toWrite [shared.RfidByteSize]byte) error {
	sess.Lock()
	defer sess.Unlock()
	err := shared.NewWriteRequest(toWrite).
		WriteTo(sess.Conn)
	if err != nil {
		println("error writing write request: " + err.Error())
		return err
	}
	err = sess.TryGetMessage(ctx, writeResponseTimeout).ProcessWriteResponse(toWrite)
	if err != nil {
		println("error processing write response: " + err.Error())
	}
	return err
}
