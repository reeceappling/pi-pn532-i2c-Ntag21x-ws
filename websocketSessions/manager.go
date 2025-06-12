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
}

var defaultCleanupFrequency = 5 * time.Minute

func NewSessionManager(cleanupFrequency *time.Duration, secret string) *SessionManager { // TODO: FIXME!!!!!
	mgr := &SessionManager{
		sessions: map[shared.RfidReaderName]*sessions.Session{},
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
					if session.Expires.Before(time.Now()) {
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

func (mgr *SessionManager) ValidateSignupRequest(reqMsg shared.ReceivedMsg) (req shared.SignupRequest, err error) {
	if reqMsg.Err != nil {
		return shared.SignupRequest{}, reqMsg.Err
	}
	if mgr == nil {
		return shared.SignupRequest{}, ErrNoSessionManager
	}
	var reqBytes []byte
	reqBytes, err = reqMsg.GetResponseData(shared.MessageTypeSignup, shared.FirstByteSignup)
	if err != nil {
		return
	}
	err = json.Unmarshal(reqBytes, &req)
	if err != nil {
		return shared.SignupRequest{}, err // TODO: ok?
	}
	return
}

func (mgr *SessionManager) ReadRfid(readerName shared.RfidReaderName) ([shared.RfidByteSize]byte, error) {
	if mgr == nil {
		return [shared.RfidByteSize]byte{}, ErrNoSessionManager
	}
	sess, exists := mgr.sessions[readerName]
	if !exists {
		return [shared.RfidByteSize]byte{}, utils.NotFound
	}
	return sess.TryReadRFID()
}

func (mgr *SessionManager) WriteRfid(readerName shared.RfidReaderName, toWrite [shared.RfidByteSize]byte) error {
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
func (mgr *SessionManager) Add(ctx context.Context, s *sessions.Session, req shared.SignupRequest) (err error) {
	if mgr == nil {
		return ErrNoSessionManager
	}
	if !mgr.SecretValid(req.Secret) {
		return ErrSecretMismatch
	}
	if existingSession, exists := mgr.sessions[req.Name]; exists { // TODO: rwMutex for this???????
		err = existingSession.TryRenew(string(req.Name), mgr.secret)
		// TODO: check old session and close if it is not working?
		return errors.New("session already exists") // TODO: MOVE?
	}
	s.Managed = true // So we don't close early
	mgr.sessions[req.Name] = s

	err = shared.NewSignupResponse(req.Name).WriteTo(s.Conn)
	if err != nil {
		delete(mgr.sessions, req.Name)
		s.ExpiryTimer.Stop()
		s.Managed = false
		s.Close() // TODO: ensure used right
		return err
	}

	go func() { // TODO: ensure this is all doing things correctly
		defer func() {
			s.ExpiryTimer.Stop()
			delete(mgr.sessions, req.Name)
			s.Managed = false
		}()

		for {
			select {
			case <-s.ExpiryTimer.C:
				// TODO: retries?
				err = s.TryRenew(string(req.Name), mgr.secret) // This will close it if renew fails enough
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
	defer conn.Close()

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
	_ = <-ctx.Done() // Keep request alive until ready to close connection
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
		//return r.Header.Get("Origin") == "<http://yourdomain.com>" // TODO: this to protect against Cross-Site websocket hijacking (CSWSH)
	},
}
