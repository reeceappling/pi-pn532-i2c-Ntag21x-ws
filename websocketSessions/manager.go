package websocketSessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/reeceappling/goUtils/v2/utils"
	"github.com/reeceappling/pi-pn532-i2c-Ntag21x-ws/v2/websocketSessions/sessions"
	"github.com/reeceappling/pi-pn532-i2c-Ntag21x-ws/v2/websocketSessions/sessions/genericsessions"
	"github.com/reeceappling/pi-pn532-i2c-Ntag21x-ws/v2/websocketSessions/shared"
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
}

var defaultCleanupFrequency = 5 * time.Minute

func NewSessionManager(cleanupFrequency *time.Duration, secret string) *SessionManager { // TODO: FIXME!!!!!
	freq := utils.Default(cleanupFrequency, defaultCleanupFrequency)
	mgr := &SessionManager{
		sessions: map[shared.RfidReaderName]*sessions.Session{},
		secret:   secret,
		cleanup:  time.NewTicker(freq), // TODO: keep or no?
		done:     make(chan struct{}),  // TODO: keep or no?
		RWMutex:  &sync.RWMutex{},
	}

	go func() {
		defer func() {
			mgr.cleanup.Stop()
			mgr.Lock()
			wg := &sync.WaitGroup{}
			wg.Add(len(mgr.sessions))
			for _, session := range mgr.sessions {
				go func() {
					session.Close()
					wg.Done()
				}()
			}
			mgr.Unlock()
			wg.Wait()
			println("finished cleaning up session manager")
			// TODO: ensure anything waiting for locks (like Add) will not work anymore after this
		}()
		for {
			select {
			case <-mgr.cleanup.C: // TODO: rename so we know this tries to renew as well as cleanup
				mgr.cleanup.Stop()
				mgr.RLock()

				wgStarted := &sync.WaitGroup{}
				wgEnded := &sync.WaitGroup{}
				wgStarted.Add(len(mgr.sessions))
				wgEnded.Add(len(mgr.sessions))
				for name, session := range mgr.sessions {
					go func() {
						wgStarted.Done()
						defer wgEnded.Done()
						now := time.Now()
						if session.Expires.Before(now) { // TODO: rework this to grab a full lock, figure out everything that needs changes (spread out on a channel), then do all the changes at once, then release the lock, to ensure no lock contention
							// TODO: pretty sure the map lock and the session-level lock will have issues here
							errr := session.TryRenew(string(name), mgr.secret) // TODO: ensure this is ok
							if errr != nil {
								println(errr.Error()) // TODO: ok?
							}
						}
					}()
				}
				wgStarted.Wait()
				mgr.RUnlock()
				wgEnded.Wait()
				mgr.cleanup.Reset(freq)
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

func (mgr *SessionManager) GetSession(name shared.RfidReaderName) (session *sessions.Session, err error) {
	if mgr == nil {
		return nil, ErrNoSessionManager
	}
	mgr.RLock()
	s, exists := mgr.sessions[name]
	mgr.RUnlock()
	if !exists {
		err = genericsessions.ErrSessionNotFound
	}
	//if s.Expires.Before(now) { // TODO: cleanup covers this
	//	// delete the session
	//	s.Close()
	//}
	return s, err
}
func (mgr *SessionManager) Sessions() []string {
	if mgr == nil {
		return nil
	}
	mgr.RLock()
	defer mgr.RUnlock()
	out := make([]string, 0, len(mgr.sessions))
	for name, _ := range mgr.sessions {
		out = append(out, string(name))
	}
	return out
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
		return shared.SignupRequest{}, err
	}
	return
}

func (mgr *SessionManager) ReadRfid(ctx context.Context, readerName shared.RfidReaderName) ([shared.RfidByteSize]byte, error) {
	sess, err := mgr.GetSession(readerName)
	if err != nil {
		return [shared.RfidByteSize]byte{}, err
	}
	return sess.TryReadRFID(ctx)
}

func (mgr *SessionManager) WriteRfid(ctx context.Context, readerName shared.RfidReaderName, toWrite [shared.RfidByteSize]byte) error {
	sess, err := mgr.GetSession(readerName)
	if err != nil {
		return err
	}
	return sess.TryWriteRFID(ctx, toWrite)
}

func (mgr *SessionManager) Delete(sessionName shared.RfidReaderName) {
	mgr.Lock()
	defer mgr.Unlock()
	delete(mgr.sessions, sessionName)
}

func (mgr *SessionManager) Add(cancellableCtx context.Context, s *sessions.Session, req shared.SignupRequest) (err error) {
	if mgr == nil {
		return ErrNoSessionManager
	}
	if !mgr.SecretValid(req.Secret) {
		return ErrSecretMismatch
	}
	mgr.Lock()
	defer mgr.Unlock()
	if existingSession, exists := mgr.sessions[req.Name]; exists {
		err = existingSession.TryRenew(string(req.Name), mgr.secret)
		existingSession.Close() // TODO: ok?
		// TODO: check old session and close if it is not working?
		return errors.New("session already exists") // TODO: MOVE?
	}

	err = shared.NewSignupResponse(req.Name).WriteTo(s.Conn)
	if err != nil {
		s.Close()
		return err
	}
	mgr.sessions[req.Name] = s
	s.Close = func() {
		mgr.Delete(req.Name)
		// TODO: close the connection???
		s.Conn.Close()
		println("session ended: " + req.Name)
	}

	//go func() { // TODO: ensure this is all doing things correctly
	//	defer func() {
	//		//s.ExpiryTimer.Stop()
	//		delete(mgr.sessions, req.Name)
	//		s.Managed = false
	//	}()
	//
	//	for {
	//		select {
	//		case <-s.ExpiryTimer.C:
	//			// TODO: retries?
	//			err = s.TryRenew(string(req.Name), mgr.secret) // This will close it if renew fails enough
	//		case <-cancellableCtx.Done():
	//			return
	//		}
	//	}
	//}()
	return nil
}

func (mgr *SessionManager) Cleanup() {
	close(mgr.done)
}

//func (mgr *SessionManager) Sessions() []string {
//	if mgr == nil {
//		return []string{}
//	}
//	mgr.RLock()
//	defer mgr.RUnlock()
//	result := slices.Map(maps.Keys(mgr.sessions), func(n shared.RfidReaderName) string {
//		return string(n)
//	})
//	return result
//}

/// TODO: TRYING OUT STUFF DOWN HERE!!!!

func ServerHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mgr := GetSessionManager(ctx)
	if mgr == nil {
		println("no session mgr:" + ErrNoSessionManager.Error())
		http.Error(w, "no session mgr:"+ErrNoSessionManager.Error(), http.StatusInternalServerError)
		return
	}
	println("upgrading connection") // TODO: del
	conn, errUpgr := upgrader.Upgrade(w, r, nil)
	if errUpgr != nil {
		println("Error upgrading connection:", errUpgr.Error())
	}
	//defer conn.Close()

	//try to read and validate format of signup message
	println("validating signup") // TODO: del
	// TODO: HANGING HERE!
	req, err := mgr.ValidateSignupRequest(shared.TryGetMessage(ctx, conn))
	if err != nil {
		return // TODO: ok?
	}
	timeBtwnChecks := 30 * time.Second // TODO: ok?
	maxFailures := 1                   // TODO: ok?
	requestTimeout := 10 * time.Second // TODO: ok?
	sessionTimeout := 5 * time.Minute  // TODO: ok?
	newSession := sessions.New(conn, &sessionTimeout, &requestTimeout, &timeBtwnChecks, &maxFailures)
	err = mgr.Add(ctx, newSession, req)
	if err != nil {
		fmt.Println("Error adding websocket session:", err)
		return
	}
	// TODO: reenable if not working _ = <-ctx.Done() // Keep request alive until ready to close connection
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
		//return r.Header.Get("Origin") == "mush.appli.ng" // TODO: make dynamic // TODO: this to protect against Cross-Site websocket hijacking (CSWSH)
	},
}
