package websocketSessions

import (
	"context"
	"encoding/json"
	"errors"
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
	MessageTypeError
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

	go func() {
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

type Session struct {
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
func (sess *Session) End() {
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

type msgResp struct {
	msgType int
	bytes   []byte
	err     error
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

	err := s.Conn.WriteMessage(websocket.PingMessage, []byte{})
	if err != nil {
		s.processRenewalFailure()
		return err
	}
	// READ for a ping message (within the allowed timeframe)
	msgType, _, err := s.TryGetMessage()
	if err != nil {
		s.processRenewalFailure()
		return errors.Join(errors.New("ping failed, erroneous response"), err)
	}
	if msgType != websocket.PingMessage {
		s.processRenewalFailure()
		return errors.New("bad ping response")
	}
	s.processSuccessfulRenewal()
	return nil
}

func (s *Session) SetSessionExpiration(t time.Time) {
	s.expires = t
}

func (sess *Session) TryGetMessage() (int, []byte, error) {
	resultChan := make(chan msgResp, 1)
	go func() {
		msgType, bytes, err := sess.Conn.ReadMessage()
		resultChan <- msgResp{msgType, bytes, err}
	}()
	select {
	case res := <-resultChan:
		return res.msgType, res.bytes, res.err
	case <-time.After(sess.requestTimeout):
		return 0, nil, errors.New("timeout") // TODO: move timeout error
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
	err := sess.Conn.WriteMessage(websocket.TextMessage, []byte(ReadEndpt))
	if err != nil {
		return out, err
	}
	msgType, msgBytes, err := sess.TryGetMessage()
	if err != nil {
		return out, err
	}
	if msgType != websocket.TextMessage {
		return out, errors.New("unexpected message type for read response: " + string(msgBytes))
	}
	return [8]byte(msgBytes), nil
}

func (sess *Session) TryWriteRFID(toWrite [8]byte) error {
	sess.mutex.Lock()
	defer sess.mutex.Unlock()
	err := NewMsg().
		WithType(MessageTypeWrite).
		WithData(toWrite[:]).
		WriteTo(sess.Conn)
	if err != nil {
		return err
	}
	msgType, msgBytes, err := sess.TryGetMessage()
	if err != nil {
		return err
	}
	if msgType != websocket.TextMessage {
		return errors.New("unexpected message type for read response")
	}
	if string(msgBytes) != string(toWrite[:]) {
		return errors.New("unexpected message was written and returned")
	}
	return nil
}

func ClientSignup(conn *websocket.Conn, name, secret string) error { // TODO: DO THIS!!!
	// send signup message
	bytes, _ := json.Marshal(SignupRequest{
		Name:   RfidReaderName(name),
		Secret: secret,
	})
	err := NewMsg().
		WithType(MessageTypeSignup).
		WithData(bytes).
		WriteTo(conn)
	if err != nil {
		return err
	}
	// try to recieve response message (or time-out)
	resultChan := make(chan msgResp, 1)
	go func() {
		msgType, bytes, err := conn.ReadMessage()
		resultChan <- msgResp{msgType, bytes, err}
	}()
	select {
	case res := <-resultChan:
		msgType, msgBytes := res.msgType, res.bytes
		if res.err != nil {
			return errors.Join(errors.New("error reading signup response from websocket on client"), res.err)
		}
		// validate response is as expected
		if msgType != websocket.BinaryMessage {
			return errors.New("unexpected message format for signup response")
		}
		resp := &SocketMessage{}
		if err = json.Unmarshal(msgBytes, resp); err != nil {
			return err
		}
		if resp.Type != MessageTypeSignup {
			return errors.New("unexpected message type for signup response")
		}
		if string(resp.Data) != name {
			return errors.New("signup response data does not match requested")
		}
	case <-time.After(3 * time.Second):
		return errors.New("timeout") // TODO: move timeout error
	}
	return nil
}
