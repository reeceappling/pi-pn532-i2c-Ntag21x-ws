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

type FullSession[T any, U comparable] struct {
	Id     U
	Data   T
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

func NewMap[T any, U comparable](ttl time.Duration) Map[T, U] {
	return Map[T, U]{
		sessions: sync.Map{},
		ttl:      ttl,
	}
}

// Map should not be created as sessions.Map{}. Create with sessions.NewMap(ttl)
type Map[T any, U comparable] struct {
	sessions sync.Map //map[string]session
	ttl      time.Duration
}

func (sm *Map[T, U]) GetSession(id U) utils.Result[Session[T]] {
	sessObj, ok := sm.sessions.Load(id)
	if !ok {
		return utils.ErroredResult[Session[T]](ErrSessionNotFound)
	}
	sess, ok := sessObj.(Session[T])
	if !ok {
		return utils.ErroredResult[Session[T]](ErrBadSession)
	}
	if sess.Expiry.Before(time.Now()) {
		sm.sessions.Delete(id)
		return utils.ErroredResult[Session[T]](ErrExpired)
	}
	// Update session expiry
	return utils.SuccessfulResult(sm.AddSession(id, sess))
}

func (sm *Map[T, U]) RefreshSession(id U, sess Session[T]) Session[T] {
	return sm.AddSession(id, sess)
}

func (sm *Map[T, U]) AddSession(id U, sess Session[T]) Session[T] {
	updatedSess := sess.WithUpdatedExpiry(sm.ttl)
	sm.sessions.Store(id, updatedSess)
	return updatedSess
}

func (sm *Map[T, U]) NewSession(id U, data T) (Session[T], error) { // TODO: DONT OVERWRITE AN EXISTING SESSION!
	if _, exists := sm.sessions.Load(id); exists {
		return Session[T]{}, errors.New("session with that ID already exists")
	}
	updatedSess := Session[T]{
		Data:   data,
		Expiry: time.Time{},
	}.WithUpdatedExpiry(sm.ttl)
	sm.sessions.Store(id, updatedSess)
	return updatedSess, nil
}

func (sm *Map[T, U]) ClearOldSessions() {
	now := time.Now()
	sm.sessions.Range(func(key, value interface{}) bool {
		sess, ok := value.(Session[T])
		if !ok || sess.Expiry.Before(now) {
			sm.sessions.Delete(key)
		}
		return true
	})
}
