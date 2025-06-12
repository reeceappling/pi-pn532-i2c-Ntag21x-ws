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
	ErrNoSessionManager     = errors.New("session manager not found. Server is improperly configured")
	ErrSecretMismatch       = errors.New("secret mismatch")
	ErrSessionAlreadyExists = errors.New("a session by that name already exists")
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
	Type int    `json:"type"` // Always binary, ping, pong, or string(error)
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
		return errors.Join(errors.New("failed to marshal socket message to Bytes"), err)
	}
	err = c.WriteMessage(websocket.BinaryMessage, respBytes)
	if err != nil {
		return errors.Join(errors.New("failed to write socket message to connection"), err)
	}
	return nil
}

type SessionManager struct {
	sessions map[RfidReaderName]*Session
	secret   string
	cleanup  *time.Ticker
	done     chan struct{}
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
			defer mgr.cleanup.Stop() // TODO: reevaluate?
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

func GetSessionManager(ctx context.Context) *SessionManager {
	mgr, ok := ctx.Value(sessionManagerContextKey).(*SessionManager)
	if !ok {
		return nil
	}
	return mgr
}

func (mgr *SessionManager) ValidateSignupRequest(reqMsg ReceivedMsg) (req SignupRequest, err error) {
	if reqMsg.Err != nil {
		return SignupRequest{}, reqMsg.Err
	}
	if mgr == nil {
		return SignupRequest{}, ErrNoSessionManager
	}
	var reqBytes []byte
	reqBytes, err = reqMsg.getResponseData(MessageTypeSignup, firstByteSignup)
	if err != nil {
		return
	}
	err = json.Unmarshal(reqBytes, &req)
	if err != nil {
		return SignupRequest{}, err // TODO: ok?
	}
	return
}

func (mgr *SessionManager) ReadRfid(readerName RfidReaderName) ([RfidByteSize]byte, error) {
	if mgr == nil {
		return [RfidByteSize]byte{}, ErrNoSessionManager
	}
	sess, exists := mgr.sessions[readerName]
	if !exists {
		return [RfidByteSize]byte{}, utils.NotFound
	}
	return sess.TryReadRFID()
}

func (mgr *SessionManager) WriteRfid(readerName RfidReaderName, toWrite [RfidByteSize]byte) error {
	if mgr == nil {
		return ErrNoSessionManager
	}
	sess, exists := mgr.sessions[readerName]
	if !exists {
		return utils.NotFound // TODO: move this!
	}
	return sess.TryWriteRFID(toWrite)
}

// TODO: ctx in Add so we can .Done()?
func (mgr *SessionManager) Add(ctx context.Context, s *Session, req SignupRequest) (err error) {
	if mgr == nil {
		return ErrNoSessionManager
	}
	if !mgr.SecretValid(req.Secret) {
		return ErrSecretMismatch
	}
	if existingSession, exists := mgr.sessions[req.Name]; exists { // TODO: rwMutex for this???????
		err = existingSession.tryRenew(string(req.Name), mgr.secret)
		// TODO: check old session and close if it is not working?
		return errors.New("session already exists") // TODO: MOVE?
	}
	s.managed = true // So we don't close early
	mgr.sessions[req.Name] = s

	err = NewSignupResponse(req.Name).WriteTo(s.Conn)
	if err != nil {
		delete(mgr.sessions, req.Name)
		s.expiryTimer.Stop()
		s.managed = false
		s.close() // TODO: ensure used right
		return err
	}

	go func() { // TODO: ensure this is all doing things correctly
		defer func() {
			s.expiryTimer.Stop()
			delete(mgr.sessions, req.Name)
			s.managed = false
		}()

		for {
			select {
			case <-s.expiryTimer.C:
				// TODO: retries?
				err = s.tryRenew(string(req.Name), mgr.secret) // This will close it if renew fails enough
			case <-ctx.Done():
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
	close            context.CancelFunc
	mutex            *sync.Mutex
	managed          bool
	ttl              time.Duration
	expiryTimer      *time.Timer
	failedChecks     int
}

func NewSession(cancelFunc context.CancelFunc, conn *websocket.Conn, sessionTTL *time.Duration, reqTimeout *time.Duration, timeBtwnChecks *time.Duration, maxCheckFailures *int) *Session { // TODO: before destroying session, lock
	sessionTimeout := utils.Default(sessionTTL, 5*time.Minute)
	timeout := utils.Default(reqTimeout, 30*time.Second)
	return &Session{
		Conn:             conn,
		ttl:              sessionTimeout,
		maxCheckFailures: utils.Default(maxCheckFailures, 0),
		expires:          time.Now().Add(timeout), // TODO: ensure this is ok
		requestTimeout:   timeout,
		mutex:            &sync.Mutex{},
		close:            cancelFunc,
		managed:          false,
		expiryTimer:      time.NewTimer(sessionTimeout),
		failedChecks:     0,
	}
}

// Does nothing if a session has not been added to a SessionManager or has been closed
func (sess *Session) End() { // TODO: reevaluate
	if sess.managed {
		sess.close()
	}
}

type ReceivedMsg struct {
	MsgType int
	Bytes   []byte
	Err     error
}

func (res ReceivedMsg) ValidateSignupResponse(expName string) error {
	if res.Err != nil {
		return res.Err
	}
	resp, err := res.getResponseData(websocket.BinaryMessage, firstByteSignup)
	if err != nil {
		return err
	}
	if string(resp[1:]) != expName {
		return errors.New("signup response data does not match requested")
	}
	return nil
}

const RfidByteSize = 8 // TODO: is this correct?? must match RfidByteSize in mdb.go
func (res ReceivedMsg) ValidateWriteRequest() (toWrite [RfidByteSize]byte, err error) {
	if res.Err != nil {
		return [RfidByteSize]byte{}, res.Err
	}
	resp, err := res.getResponseData(websocket.BinaryMessage, FirstByteWrite)
	if err != nil {
		return [RfidByteSize]byte{}, err
	}
	if len(resp) != RfidByteSize {
		return [RfidByteSize]byte{}, errors.New("invalid write request size")
	}
	return [RfidByteSize]byte(resp), nil
}

func (res ReceivedMsg) validateWriteResponse(expectedWritten [RfidByteSize]byte) error {
	resp, err := res.getResponseData(websocket.BinaryMessage, FirstByteWrite)
	if err != nil {
		return err
	}
	if string(expectedWritten[:]) != string(resp) {
		return errors.New("bad response content, wrote wrong Bytes")
	}
	return nil
}

func (res *ReceivedMsg) ValidateReadRequest() error {
	_, err := res.getResponseData(websocket.BinaryMessage, FirstByteRead)
	if err != nil {
		return err
	}
	return nil
}

func (res ReceivedMsg) processReadResponse() (bytesRead [RfidByteSize]byte, err error) {
	out := [RfidByteSize]byte{}
	resp, err := res.getResponseData(websocket.BinaryMessage, FirstByteRead)
	if len(resp) != RfidByteSize {
		return out, errors.New("bad read response size")
	}
	return [RfidByteSize]byte(resp), nil
}

func (res ReceivedMsg) getResponseData(expMsgType int, expFirstByte byte) (resultWithTypeByte []byte, err error) {
	resultWithTypeByte = nil
	msgType, msgBytes := res.MsgType, res.Bytes
	if res.Err != nil {
		err = errors.Join(errors.New("error reading response from websocket on client"), res.Err)
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
	if msgType != expMsgType {
		err = errors.New("unexpected message format for response")
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

func (s *Session) processSuccessfulRenewal() {
	s.SetSessionExpiration(time.Now().Add(s.ttl)) // TODO: may now be unnecessary
	s.failedChecks = 0
	s.expiryTimer.Reset(s.ttl)
}
func (s *Session) processRenewalFailure() {
	s.failedChecks++
	if s.failedChecks > s.maxCheckFailures { // TODO: ok?
		s.close()
		return
	}
	s.SetSessionExpiration(time.Now().Add(s.ttl)) // TODO: may now be unnecessary
	s.expiryTimer.Reset(s.ttl)
}

func (s *Session) tryRenew(name, expSecret string) (renewErr error) {
	s.mutex.Lock()
	s.expiryTimer.Stop()
	defer s.mutex.Unlock()

	err := NewRenewalRequest(name).WriteTo(s.Conn) // TODO: pong handler?
	if err != nil {
		s.processRenewalFailure()
		return err
	}
	// READ for a ping message (within the allowed timeframe)
	err = s.TryGetMessage().validateRenewalResponse(expSecret)
	if err != nil {
		s.processRenewalFailure()
		return errors.Join(errors.New("failed to renew client lease"), err)
	}
	s.processSuccessfulRenewal()
	return nil
}

func (s *Session) SetSessionExpiration(t time.Time) {
	s.expires = t
}

func (sess *Session) TryGetMessage() ReceivedMsg {
	timedCtx, cancel := context.WithTimeout(context.Background(), sess.requestTimeout)
	defer cancel()
	return TryGetMessage(timedCtx, sess.Conn)
}

func TryGetMessage(ctx context.Context, conn *websocket.Conn) ReceivedMsg {
	resultChan := make(chan ReceivedMsg, 1)
	go func() {
		defer close(resultChan)
		msgType, bytes, err := conn.ReadMessage()
		resultChan <- ReceivedMsg{msgType, bytes, err}
	}()
	select {
	case res := <-resultChan:
		return res
	case <-ctx.Done():
		return ReceivedMsg{
			MsgType: MessageTypeError,
			Bytes:   nil,
			Err:     ErrGetMessageTimeout,
		}
	}
}

var ErrGetMessageTimeout = errors.New("timeout")

func (mgr *SessionManager) Sessions() []string {
	if mgr == nil {
		return []string{}
	}
	return slices.Map(maps.Keys(mgr.sessions), func(n RfidReaderName) string {
		return string(n)
	})
}

func (sess *Session) TryReadRFID() ([RfidByteSize]byte, error) {
	sess.mutex.Lock()
	defer sess.mutex.Unlock()
	out := [RfidByteSize]byte{}
	err := NewReadRequest().WriteTo(sess.Conn)
	if err != nil {
		return out, err
	}
	return sess.TryGetMessage().processReadResponse()
}

func (sess *Session) TryWriteRFID(toWrite [RfidByteSize]byte) error { // TODO: this is client side
	sess.mutex.Lock()
	defer sess.mutex.Unlock()
	err := NewWriteRequest(toWrite).WriteTo(sess.Conn)
	if err != nil {
		return err
	}
	return sess.TryGetMessage().validateWriteResponse(toWrite)
}

func ClientSignup(conn *websocket.Conn, name, secret string) error {
	// send signup message
	err := NewSignupRequest(name, secret).WriteTo(conn)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // TODO: ensure timeout ok
	defer cancel()
	return TryGetMessage(ctx, conn).ValidateSignupResponse(name)
}

/// TODO: TRYING OUT STUFF DOWN HERE!!!!

const (
	firstByteSignup uint8 = iota
	FirstByteRead
	FirstByteWrite
	firstByteRenew
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
		Data: []byte{FirstByteRead},
	}
}
func NewReadResponse(r [RfidByteSize]byte) *SocketMessage {
	return &SocketMessage{
		Type: websocket.BinaryMessage,
		Data: append([]byte{FirstByteRead}, r[:]...),
	}
}
func NewWriteRequest(w [RfidByteSize]byte) *SocketMessage { // TODO: req/res are the same for this one
	return &SocketMessage{
		Type: websocket.BinaryMessage,
		Data: append([]byte{FirstByteWrite}, w[:]...),
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
func NewSignupResponse(clientName RfidReaderName) *SocketMessage { // TODO: do we even need this?
	return &SocketMessage{
		Type: websocket.BinaryMessage,
		Data: append([]byte{firstByteSignup}, []byte(clientName)...),
	}
}
func NewRenewalRequest(readerName string) *SocketMessage {
	return &SocketMessage{
		Type: websocket.PingMessage, // TODO: is this ok?
		Data: append([]byte{firstByteRenew}, []byte(readerName)...),
	}
}
func NewRenewalResponse(secret string) *SocketMessage { // TODO: USE THIS
	return &SocketMessage{
		Type: websocket.PongMessage,
		Data: append([]byte{firstByteRenew}, []byte(secret)...),
	}
}
func (res ReceivedMsg) ValidateRenewalRequest(expName string) error {
	return res.genericValidate(websocket.PingMessage, firstByteRenew, expName, "renewal request")
}

func (res ReceivedMsg) validateRenewalResponse(expSecret string) error {
	return res.genericValidate(websocket.PongMessage, firstByteRenew, expSecret, "renewal response")
}

func (res ReceivedMsg) genericValidate(expMsgType int, expFirstByte uint8, exStr string, what string) error {
	resp, err := res.getResponseData(expMsgType, expFirstByte)
	if err != nil {
		return err
	}
	if string(resp) != exStr {
		return fmt.Errorf(`received incorrect secret on %s`, what)
	}
	return nil
}

func ServerHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mgr := GetSessionManager(ctx)
	if mgr == nil {
		http.Error(w, ErrNoSessionManager.Error(), http.StatusInternalServerError)
		return
	}
	conn, errUpgr := upgrader.Upgrade(w, r, nil)
	if errUpgr != nil {
		fmt.Println("Error upgrading connection:", errUpgr)
	}
	defer conn.Close()

	//try to read and validate format of signup message
	req, err := mgr.ValidateSignupRequest(TryGetMessage(ctx, conn))
	if err != nil {
		return // TODO: ok?
	}
	timeBtwnChecks := 30 * time.Second // TODO: ok?
	maxFailures := 1                   // TODO: ok?
	requestTimeout := 10 * time.Second // TODO: ok?
	sessionTimeout := 5 * time.Minute  // TODO: ok?
	ctx, sessionCancelFunc := context.WithCancel(r.Context())
	newSession := NewSession(sessionCancelFunc, conn, &sessionTimeout, &requestTimeout, &timeBtwnChecks, &maxFailures)
	err = mgr.Add(ctx, newSession, req)
	if err != nil {
		fmt.Println("Error adding websocket session:", err)
		return
	}
	_ = <-ctx.Done() // Keep request alive until ready to close connection
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
		//return r.Header.Get("Origin") == "<http://yourdomain.com>" // TODO: this to protect against Cross-Site websocket hijacking (CSWSH)
	},
}
