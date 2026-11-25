package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	BcryptCost   = 10
	SessionTTL   = 24 * time.Hour
	SessionToken = 32
)

type Session struct {
	Token   string
	Expires time.Time
}

type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string]*Session)}
}

func (s *SessionStore) Create() (string, error) {
	buf := make([]byte, SessionToken)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[tok] = &Session{Token: tok, Expires: time.Now().Add(SessionTTL)}
	return tok, nil
}

func (s *SessionStore) Validate(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.RLock()
	sess, ok := s.sessions[tok]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(sess.Expires) {
		s.mu.Lock()
		delete(s.sessions, tok)
		s.mu.Unlock()
		return false
	}
	return true
}

func (s *SessionStore) Revoke(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tok)
}

func (s *SessionStore) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = make(map[string]*Session)
}

// HashPassword returns bcrypt hash of pw, requiring length >= 4.
func HashPassword(pw string) (string, error) {
	if len(pw) < 4 {
		return "", errors.New("password must be at least 4 characters")
	}
	if len(pw) > 72 {
		// bcrypt has a 72-byte limit
		return "", errors.New("password must be at most 72 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), BcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}
