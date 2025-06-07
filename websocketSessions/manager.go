package websocketSessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/reeceappling/goUtils/v2/utils"
	"github.com/reeceappling/goUtils/v2/utils/slices"
	"golang.org/x/exp/maps"
	"net/http"

	"sync"
	"time"
)

const (
	MessageTypeSignup = iota
	MessageTypeError  //1
	MessageTypeWrite
)

const (
	ReadEndpt = "read"
)

var (
	ErrNoSessionManager = errors.New("session manager not found. Server is improperly configured")
	ErrSecretMismatch   = errors.New("secret mismatch")
)

type SignupRequest struct {
	Name   RfidReaderName
	Secret string
}

type RfidReaderName string

func NewMsg() *SocketMessage {
	return &SocketMessage{}
}

type SocketMessage struct {
	Type int    `json:"type"` // TODO: msg type as int?
	Data []byte `json:"data,omitempty"`
}

func (sockMsg *SocketMessage) WithData(data []byte) *SocketMessage {
	sockMsg.Data = data
	return sockMsg
}
func (sockMsg *SocketMessage) WithType(msgType int) *SocketMessage {
	sockMsg.Type = msgType
	return sockMsg
}
func (sockMsg *SocketMessage) WriteTo(c *websocket.Conn) error {
	respBytes, err := json.Marshal(*sockMsg) // Error should be impossible here
	if err != nil {
		return errors.Join(errors.New("failed to marshal socket message to bytes"), err)
	}
	err = c.WriteMessage(websocket.BinaryMessage, respBytes)
	if err != nil {
		return errors.Join(errors.New("failed to write socket message to connection"), err)
	}
	return nil
}

var defaultCleanupFrequency = 5 * time.Minute

func NewSessionManager(cleanupFrequency *time.Duration, secret string) *SessionManager { // TODO: FIXME!!!!!
	mgr := &SessionManager{
		sessions: map[RfidReaderName]*Session{},
		secret:   secret,
		cleanup:  time.NewTicker(utils.Default(cleanupFrequency, defaultCleanupFrequency)), // TODO: keep or no?
		done:     make(chan struct{}),                                                      // TODO: keep or no?
	}

	go func() {
		defer func() {
			defer mgr.cleanup.Stop()
			for _, session := range mgr.sessions {
				session.End()
			}
		}()
		for {
			select {
			case <-mgr.cleanup.C:
				// TODO: clean up all sessions
				for _, session := range mgr.sessions {
					if session.expires.Before(time.Now()) {
						session.End()
					}
				}
			case _, ok := <-mgr.done:
				if !ok {
					return
				}
			}
		}
	}()
	return mgr
}

type SessionManager struct {
	sessions map[RfidReaderName]*Session
	secret   string
	cleanup  *time.Ticker
	done     chan struct{}
}

func (mgr *SessionManager) SecretValid(secret string) bool {
	if mgr == nil {
		return false
	}
	return (*mgr).secret == secret
}

func (mgr *SessionManager) Middleware() func(http.Handler) http.Handler {
	if mgr == nil {
		return func(childHandlerFunc http.Handler) http.Handler {
			return childHandlerFunc
		}
	}
	return func(childHandlerFunc http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), sessionManagerContextKey, mgr)
			childHandlerFunc.ServeHTTP(w, r.WithContext(ctx))
		})
	}

}

const sessionManagerContextKey = "rfidSessionManager"

func GetSessionManager(ctx context.Context) *SessionManager { // TODO: private?
	mgr, ok := ctx.Value(sessionManagerContextKey).(*SessionManager)
	if !ok {
		return nil
	}
	return mgr
}

func (mgr *SessionManager) ReadRfid(readerName RfidReaderName) ([8]byte, error) {
	if mgr == nil {
		return [8]byte{}, ErrNoSessionManager
	}
	sess, exists := mgr.sessions[readerName]
	if !exists {
		return [8]byte{}, utils.NotFound
	}
	return sess.TryReadRFID()
}

func (mgr *SessionManager) WriteRfid(readerName RfidReaderName, toWrite [8]byte) error {
	if mgr == nil {
		return ErrNoSessionManager
	}
	sess, exists := mgr.sessions[readerName]
	if !exists {
		return utils.NotFound // TODO: move this!
	}
	return sess.TryWriteRFID(toWrite)
}

func (mgr *SessionManager) Add(sessionCancelFunc context.CancelFunc, s *Session, req SignupRequest) (err error) {
	if mgr == nil {
		return ErrNoSessionManager
	}
	if !mgr.SecretValid(req.Secret) {
		return ErrSecretMismatch
	}
	name := req.Name
	if existingSession, exists := mgr.sessions[name]; exists { // TODO: rwMutex for this???????
		err = existingSession.tryRenew()
		// TODO: check old session and close if it is not working
		return errors.New("session already exists") // TODO: MOVE?
	}
	s.managed = true // So we don't close early
	mgr.sessions[name] = s

	err = NewMsg().WithType(MessageTypeSignup).WithData([]byte(req.Name)).WriteTo(s.Conn)
	if err != nil {
		delete(mgr.sessions, name)
		s.expiryTimer.Stop()
		s.managed = false
		close(s.closer)
		sessionCancelFunc()
		return err
	}

	go func() { // TODO: ensure this is all doing things correctly
		defer func() {
			s.expiryTimer.Stop()
			delete(mgr.sessions, name)
			s.managed = false
			close(s.closer)
			sessionCancelFunc()
		}()

		for {
			select {
			case <-s.expiryTimer.C:
				// TODO: retries?
				err = s.tryRenew() // This will close it if renew fails enough
			case <-s.closer:
				return
			}
		}
	}()
	return nil
}

func (mgr *SessionManager) Cleanup() {
	close(mgr.done)
}

type Session struct { // TODO: reevaluate
	Conn             *websocket.Conn
	expires          time.Time
	maxCheckFailures int
	requestTimeout   time.Duration
	closer           chan bool
	mutex            *sync.Mutex
	managed          bool
	ttl              time.Duration
	expiryTimer      *time.Timer
	failedChecks     int
}

// Does nothing if a session has not been added to a SessionManager or has been closed
func (sess *Session) End() { // TODO: reevaluate
	// TODO: use mutex
	if sess.managed {
		go func() {
			sess.closer <- true
		}()
	}
}

func NewSession(conn *websocket.Conn, sessionTTL *time.Duration, reqTimeout *time.Duration, timeBtwnChecks *time.Duration, maxCheckFailures *int) *Session { // TODO: before destroying session, lock
	sessionTimeout := utils.Default(sessionTTL, 5*time.Minute)
	timeout := utils.Default(reqTimeout, 30*time.Second)
	return &Session{
		Conn:             conn,
		ttl:              sessionTimeout,
		maxCheckFailures: utils.Default(maxCheckFailures, 0),
		expires:          time.Now().Add(timeout), // TODO: ensure this is ok
		requestTimeout:   timeout,
		mutex:            &sync.Mutex{},
		closer:           make(chan bool),
		managed:          false,
		expiryTimer:      time.NewTimer(sessionTimeout),
		failedChecks:     0,
	}
}

type ReceivedMsg struct {
	msgType int
	bytes   []byte
	err     error
}

// TODO: process signup request

func (res ReceivedMsg) validateSignupResponse(expName string) error {
	resp, err := res.getBinaryResponseData(firstByteSignup)
	if err != nil {
		return err
	}
	if string(resp[1:]) != expName {
		return errors.New("signup response data does not match requested")
	}
	return nil
}

const writeSize = 8 // TODO: is this correct??
func (res ReceivedMsg) validateWriteRequest() (toWrite []byte, err error) {
	resp, err := res.getBinaryResponseData(firstByteWrite)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (res ReceivedMsg) validateWriteResponse(expectedWritten [8]byte) error {
	resp, err := res.getBinaryResponseData(firstByteWrite)
	if err != nil {
		return err
	}
	if string(expectedWritten[:]) != string(resp) {
		return errors.New("bad response content, wrote wrong bytes")
	}
	return nil
}

func (res ReceivedMsg) validateReadRequest() error {
	_, err := res.getBinaryResponseData(firstByteRead)
	if err != nil {
		return err
	}
	return nil
}

func (res ReceivedMsg) processReadResponse() (bytesRead [writeSize]byte, err error) {
	out := [writeSize]byte{}
	resp, err := res.getBinaryResponseData(firstByteRead)
	if len(resp) != writeSize {
		return out, errors.New("bad read response size")
	}
	return [8]byte(resp), nil
}

func (res ReceivedMsg) getBinaryResponseData(expFirstByte byte) (resultWithTypeByte []byte, err error) {
	resultWithTypeByte = nil
	msgType, msgBytes := res.msgType, res.bytes
	if res.err != nil {
		err = errors.Join(errors.New("error reading response from websocket on client"), res.err)
		return
	}
	// validate response is as expected
	resp := &SocketMessage{}
	if err = json.Unmarshal(msgBytes, resp); err != nil {
		return
	}
	if msgType == websocket.TextMessage {
		err = errors.New(string(resp.Data))
		return
	}
	if msgType != websocket.BinaryMessage {
		err = errors.New("unexpected message format for signup response")
		return
	}
	if resp.Data[0] != expFirstByte {
		err = fmt.Errorf(`first byte was expected to be %d, got %d`, int(expFirstByte), resp.Data[0])
		return
	}
	if len(resp.Data) == 1 {
		return nil, nil // TODO: ok?
	}
	return resp.Data[1:], nil
}

func ensureSizePlusOneByteResponse(resp []byte) error {
	if writeSize+1 != len(resp) {
		return errors.New("invalid response size even though the response was non-erroneous")
	}
	return nil
}

func (s *Session) processSuccessfulRenewal() {
	s.SetSessionExpiration(time.Now().Add(s.ttl)) // TODO: may now be unnecessary
	s.failedChecks = 0
	s.expiryTimer.Reset(s.ttl)
}
func (s *Session) processRenewalFailure() {
	s.failedChecks++
	if s.failedChecks > s.maxCheckFailures { // TODO: ok?
		s.closer <- true
	}
	s.SetSessionExpiration(time.Now().Add(s.ttl)) // TODO: may now be unnecessary
	s.expiryTimer.Reset(s.ttl)
}

func (s *Session) tryRenew() (renewErr error) {
	s.mutex.Lock()
	s.expiryTimer.Stop()
	defer s.mutex.Unlock()

	err := s.Conn.WriteMessage(websocket.PingMessage, []byte{}) // TODO: pongHandler?
	if err != nil {
		s.processRenewalFailure()
		return err
	}
	// READ for a ping message (within the allowed timeframe)
	msg := s.TryGetMessage()
	if msg.err != nil {
		s.processRenewalFailure()
		return errors.Join(errors.New("ping failed, erroneous response"), msg.err)
	}
	if msg.msgType != websocket.PongMessage {
		s.processRenewalFailure()
		return errors.New("bad ping response")
	}
	s.processSuccessfulRenewal()
	return nil
}

func (s *Session) SetSessionExpiration(t time.Time) {
	s.expires = t
}

func (sess *Session) TryGetMessage() ReceivedMsg {
	resultChan := make(chan ReceivedMsg, 1)
	go func() {
		defer close(resultChan)
		msgType, bytes, err := sess.Conn.ReadMessage()
		resultChan <- ReceivedMsg{msgType, bytes, err}
	}()
	select {
	case res := <-resultChan:
		return res
	case <-time.After(sess.requestTimeout):
		return ReceivedMsg{
			msgType: MessageTypeError,
			bytes:   nil,
			err:     errors.New("timeout"), // TODO: move timeout error
		}
	}
}

func (mgr *SessionManager) Sessions() []string {
	if mgr == nil {
		return []string{}
	}
	return slices.Map(maps.Keys(mgr.sessions), func(n RfidReaderName) string {
		return string(n)
	})
}

func (sess *Session) TryReadRFID() ([8]byte, error) {
	sess.mutex.Lock()
	defer sess.mutex.Unlock()
	out := [8]byte{}
	err := NewReadRequest().WriteTo(sess.Conn)
	if err != nil {
		return out, err
	}
	return sess.TryGetMessage().processReadResponse()
}

func (sess *Session) TryWriteRFID(toWrite [8]byte) error { // TODO: this is client side
	sess.mutex.Lock()
	defer sess.mutex.Unlock()
	err := NewWriteRequest(toWrite).WriteTo(sess.Conn)
	if err != nil {
		return err
	}
	return sess.TryGetMessage().validateWriteResponse(toWrite)
}

func ClientSignup(conn *websocket.Conn, name, secret string) error { // TODO: DO THIS!!!
	// send signup message
	err := NewSignupRequest(name, secret).WriteTo(conn)
	if err != nil {
		return err
	}
	// try to recieve response message (or time-out)
	resultChan := make(chan ReceivedMsg, 1)
	go func() {
		msgType, bs, readErr := conn.ReadMessage()
		resultChan <- ReceivedMsg{msgType, bs, readErr}
	}()
	select {
	case res := <-resultChan:
		errResp := res.validateSignupResponse(name)
		if errResp != nil {
			return errResp
		}
	case <-time.After(3 * time.Second): // TODO: set request timeout
		return errors.New("timeout") // TODO: move timeout error
	}
	return nil
}

/// TODO: TRYING OUT STUFF DOWN HERE!!!!

const (
	firstByteSignup uint8 = iota
	firstByteRead
	firstByteWrite
)

func NewErrorResponse(err error) *SocketMessage {
	return &SocketMessage{
		Type: websocket.TextMessage,
		Data: []byte(err.Error()),
	}
}
func NewReadRequest() *SocketMessage {
	return &SocketMessage{
		Type: websocket.BinaryMessage,
		Data: []byte{firstByteRead},
	}
}
func NewWriteRequest(w [8]byte) *SocketMessage {
	return &SocketMessage{
		Type: websocket.BinaryMessage,
		Data: append([]byte{firstByteWrite}, w[:]...),
	}
}
func NewSignupRequest(readerName, secret string) *SocketMessage {
	bs, _ := json.Marshal(SignupRequest{
		Name:   RfidReaderName(readerName),
		Secret: secret,
	})
	return &SocketMessage{
		Type: websocket.BinaryMessage,
		Data: append([]byte{firstByteSignup}, bs...),
	}
}
func NewSignupResponse(signupRequestBytes []byte) *SocketMessage { // TODO: do we even need this?
	return &SocketMessage{
		Type: websocket.BinaryMessage,
		Data: signupRequestBytes,
	}
}
