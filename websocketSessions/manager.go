package websocketSessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/reeceappling/goUtils/v2/utils"
	"github.com/reeceappling/goUtils/v2/utils/slices"
	"github.com/reeceappling/pi-pn532-i2c-Ntag21x-ws/v2/websocketSessions/sessions"
	"github.com/reeceappling/pi-pn532-i2c-Ntag21x-ws/v2/websocketSessions/shared"
	"golang.org/x/exp/maps"
	"net/http"
	"sync"

	"time"
)

const (
	ReadEndpt = "read"
)

var (
	ErrNoSessionManager     = errors.New("session manager not found. Server is improperly configured")
	ErrSecretMismatch       = errors.New("secret mismatch")
	ErrSessionAlreadyExists = errors.New("a session by that name already exists")
)

type SessionManager struct {
	sessions map[shared.RfidReaderName]*sessions.Session
	secret   string
	cleanup  *time.Ticker
	done     chan struct{}
	*sync.RWMutex
	renewalQueue *sessions.RenewalQueue2
}

var defaultCleanupFrequency = 5 * time.Minute

func NewSessionManager(cleanupFrequency *time.Duration, secret string) *SessionManager { // TODO: FIXME!!!!!

	renewalQueue := &sessions.RenewalQueue2{}
	mgr := &SessionManager{
		sessions:     map[shared.RfidReaderName]*sessions.Session{},
		secret:       secret,
		cleanup:      time.NewTicker(utils.Default(cleanupFrequency, defaultCleanupFrequency)), // TODO: keep or no?
		done:         make(chan struct{}),                                                      // TODO: keep or no?
		RWMutex:      &sync.RWMutex{},
		renewalQueue: renewalQueue,
	}

	go func() {
		defer func() {
			defer mgr.cleanup.Stop() // TODO: reevaluate?
			for _, session := range mgr.sessions {
				session.End()
			}
		}()
		cleanupFreq := utils.Default(cleanupFrequency, defaultCleanupFrequency)
		for {
			qItem := renewalQueue.Head()
			if qItem == nil {
				time.Sleep(cleanupFreq) // TODO: ok? should probably be TTL
			}
			thisSess := qItem.Sess
			if timeLeft := thisSess.Expires.Sub(time.Now()); timeLeft > 0 {
				time.Sleep(timeLeft + time.Second) // TODO: ok?
				continue
			}
			readerName := "" // TODO: fic!
			var err error = nil
			for i := 0; i < 1; i++ {
				err = thisSess.TryRenew(readerName, secret)
				if err != nil {
					// retry soon
					time.Sleep(time.Second) // TODO: ok?
					continue
				}
				break
			}
			if err != nil {
				// TODO: remove from the queue because it is closed!
				renewalQueue.RemoveHead()
			} else {
				renewalQueue.MoveHeadToTail()
			}
			continue

			//select {
			//case <-mgr.cleanup.C:
			//	// TODO: clean up all sessions
			//	for _, session := range mgr.sessions {
			//		if session.Expires.Before(time.Now()) {
			//			session.End()
			//		}
			//	}
			//case _, ok := <-mgr.done:
			//	if !ok {
			//		return
			//	}
			//}
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

func (mgr *SessionManager) ValidateSignupRequest(reqMsg shared.ReceivedMsg) (req shared.SignupRequest, err error) {
	if reqMsg.Err != nil {
		return req, reqMsg.Err
	}
	if mgr == nil {
		return req, ErrNoSessionManager
	}
	var reqBytes []byte
	reqBytes, err = reqMsg.GetResponseData(shared.MessageTypeSignup, shared.FirstByteSignup)
	if err != nil {
		return
	}
	err = json.Unmarshal(reqBytes, &req)
	if err != nil {
		return req, err // TODO: ok?
	}
	return
}

func NewRfidResult() [shared.RfidByteSize]byte {
	return [shared.RfidByteSize]byte{}
}

func (mgr *SessionManager) GetSessionByName(readerName shared.RfidReaderName) (sess *sessions.Session, exists bool) {
	mgr.RLock()
	defer mgr.RUnlock()
	sess, exists = mgr.sessions[readerName]
	return sess, exists
}

func (mgr *SessionManager) ReadRfid(readerName shared.RfidReaderName) ([shared.RfidByteSize]byte, error) {
	result := NewRfidResult()
	if mgr == nil {
		return result, ErrNoSessionManager
	}
	sess, exists := mgr.GetSessionByName(readerName)
	if !exists {
		return result, utils.NotFound
	}
	return sess.TryReadRFID()
}

func (mgr *SessionManager) WriteRfid(readerName shared.RfidReaderName, toWrite [shared.RfidByteSize]byte) error {
	if mgr == nil {
		return ErrNoSessionManager
	}
	sess, exists := mgr.GetSessionByName(readerName)
	if !exists {
		return utils.NotFound // TODO: move this!
	}
	return sess.TryWriteRFID(toWrite)
}

// TODO: ctx in Add so we can .Done()?
func (mgr *SessionManager) Add(ctx context.Context, s *sessions.Session, req shared.SignupRequest) (err error) {
	if mgr == nil {
		return ErrNoSessionManager
	}
	if !mgr.SecretValid(req.Secret) {
		return ErrSecretMismatch
	}
	mgr.RLock()
	existingSession, exists := mgr.sessions[req.ReaderName]
	mgr.RUnlock()
	if exists { // TODO: rwMutex for this???????
		err = existingSession.TryRenew(string(req.ReaderName), mgr.secret)
		// TODO: check old session and close if it is not working?
		return errors.New("session already exists") // TODO: MOVE?
	}
	renewalItem := &sessions.RenewalItem{Sess: s}
	mgr.Lock()
	s.CloseFunc = func() error {
		mgr.Lock()
		defer mgr.Unlock()
		mgr.renewalQueue.RemoveItem(renewalItem)
		delete(mgr.sessions, req.ReaderName)
		s.Managed = false // TODO: what does this accomplish? Should we be checking this elsewhere?
		return s.Conn.Close()
	}

	mgr.renewalQueue.Append(renewalItem)
	s.Managed = true // So we don't close early
	mgr.sessions[req.ReaderName] = s
	mgr.Unlock()
	for i := 0; i < 1; i++ { // TODO: is multiple tries here ok?
		err = shared.NewSignupResponse(req.ReaderName).
			WriteTo(s.Conn)
		if err == nil {
			break
		}

	}
	if err != nil {
		return errors.Join(err, s.CloseFunc())
	}
	return nil
}

func (mgr *SessionManager) Cleanup() {
	close(mgr.done)
}

func (mgr *SessionManager) Sessions() []string {
	if mgr == nil {
		return []string{}
	}
	return slices.Map(maps.Keys(mgr.sessions), func(n shared.RfidReaderName) string {
		return string(n)
	})
}

/// TODO: TRYING OUT STUFF DOWN HERE!!!!

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
	defer conn.Close() // TODO: DO THIS IN THE OTHER THING
	// Do all signup logic!
	//try to read and validate format of signup message
	req, err := mgr.ValidateSignupRequest(shared.TryGetMessage(ctx, conn))
	if err != nil {
		return // TODO: ok?
	}
	timeBtwnChecks := 30 * time.Second // TODO: ok?
	maxFailures := 1                   // TODO: ok?
	requestTimeout := 10 * time.Second // TODO: ok?
	sessionTimeout := 5 * time.Minute  // TODO: ok?
	ctx, sessionCancelFunc := context.WithCancel(r.Context())
	newSession := sessions.New(sessionCancelFunc, conn, &sessionTimeout, &requestTimeout, &timeBtwnChecks, &maxFailures)
	err = mgr.Add(ctx, newSession, req)
	if err != nil {
		fmt.Println("Error adding websocket session:", err)
		return
	}
	// TODO: FIX for reading here?
	// 4. Read loop (handles client messages)
	//for {
	//	_, _, err := conn.ReadMessage()
	//	if err != nil {
	//		if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
	//			log.Printf("error: %v", err)
	//		}
	//		break
	//	}
	//}
	// TODO: request lifespan is managed in mgr.Add _ = <-ctx.Done() // Keep request alive until ready to close connection // TODO: ok?
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
		//return r.Header.Get("Origin") == "<http://yourdomain.com>" // TODO: this to protect against Cross-Site websocket hijacking (CSWSH)
	},
}
