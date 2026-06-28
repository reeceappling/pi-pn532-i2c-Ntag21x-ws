package genericsessions

import (
	"errors"
	"github.com/reeceappling/goUtils/v2/utils"
	"sync"
	"time"
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrExpired         = errors.New("session expired")
	ErrBadSession      = errors.New("bad session found in storage")
)

// TODO: consider switching other sessions to these

type FullSession[from comparable, To any] struct {
	Id     from
	Data   To
	Expiry time.Time
}

type Session[T any] struct {
	Data   T
	Expiry time.Time
}

func (sess Session[T]) WithUpdatedExpiry(ttl time.Duration) Session[T] {
	out := sess
	out.Expiry = time.Now().Add(ttl)
	return out
}

func NewMap[From comparable, To any](ttl time.Duration, cleanupCallback func(From, To) error) Map[From, To] {
	cuFunc := func(from From, to To) error { return nil }
	if cleanupCallback != nil {
		cuFunc = cleanupCallback
	}
	return Map[From, To]{
		sessions:         sync.Map{},
		ttl:              ttl,
		extraCleanupFunc: cuFunc,
	}
}

// Map should not be created as sessions.Map{}. Create with sessions.NewMap(ttl)
type Map[From comparable, To any] struct {
	sessions         sync.Map //map[string]session
	ttl              time.Duration
	extraCleanupFunc func(From, To) error // TODO: DO THIS, OR GET RID OF?
}

func (sm *Map[From, To]) GetSession(id From, refreshTTL bool) utils.Result[Session[To]] { // TODO: PUSH THIS UPDATE TO THE REPO
	sessObj, ok := sm.sessions.Load(id)
	if !ok {
		return utils.ErroredResult[Session[To]](ErrSessionNotFound)
	}
	sess, ok := sessObj.(Session[To])
	if !ok {
		return utils.ErroredResult[Session[To]](ErrBadSession)
	}
	if sess.Expiry.Before(time.Now()) {
		// TODO: CLEANUP?????
		sm.sessions.Delete(id)
		return utils.ErroredResult[Session[To]](ErrExpired)
	}
	out := sess
	if refreshTTL {
		// Update session expiry
		out = sm.AddSession(id, sess)
	}
	return utils.SuccessfulResult(out)
}

func (sm *Map[From, To]) RefreshSession(id From, sess Session[To]) Session[To] {
	return sm.AddSession(id, sess)
}

func (sm *Map[From, To]) AddSession(id From, sess Session[To]) Session[To] {
	updatedSess := sess.WithUpdatedExpiry(sm.ttl)
	sm.sessions.Store(id, updatedSess)
	return updatedSess
}

func (sm *Map[From, To]) NewSession(id From, data To) (Session[To], error) { // TODO: DONT OVERWRITE AN EXISTING SESSION!
	if _, exists := sm.sessions.Load(id); exists {
		return Session[To]{}, errors.New("session with that ID already exists")
	}
	updatedSess := Session[To]{
		Data:   data,
		Expiry: time.Time{},
	}.WithUpdatedExpiry(sm.ttl)
	sm.sessions.Store(id, updatedSess)
	return updatedSess, nil
}

func (sm *Map[From, To]) ClearOldSessions() {
	now := time.Now()
	sm.sessions.Range(func(key, value interface{}) bool {
		sess, ok := value.(Session[To])
		if !ok || sess.Expiry.Before(now) {
			sm.sessions.Delete(key)
		}
		return true
	})
}
