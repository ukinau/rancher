package remotedialer

import (
	"fmt"
	"math/rand"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rancher/rancher/pkg/metrics"
)

type sessionManager struct {
	sync.Mutex
	clients map[string][]*session
}

func newSessionManager() *sessionManager {
	return &sessionManager{
		clients: map[string][]*session{},
	}
}

func (sm *sessionManager) getByClient(clientKey string) (*session, error) {
	sm.Lock()
	defer sm.Unlock()

	sessions := sm.clients[clientKey]
	if len(sessions) > 0 {
		return sessions[0], nil
	}

	return nil, fmt.Errorf("failed to find session for client %s", clientKey)
}

func (sm *sessionManager) add(clientKey string, conn *websocket.Conn) *session {
	sessionKey := rand.Int63()
	session := newSession(sessionKey, clientKey, conn)
	session.sessionKey = sessionKey

	sm.Lock()
	defer sm.Unlock()

	sm.clients[clientKey] = append(sm.clients[clientKey], session)
	if metrics.PrometheusMetrics {
		metrics.TotalAddWS.With(
			prometheus.Labels{
				"clientkey": clientKey,
			}).Inc()
	}

	return session
}

func (sm *sessionManager) remove(s *session) {
	sm.Lock()
	defer sm.Unlock()

	var newSessions []*session

	for _, v := range sm.clients[s.clientKey] {
		if v.sessionKey == s.sessionKey {
			continue
		}
		newSessions = append(newSessions, v)
	}

	if len(newSessions) == 0 {
		delete(sm.clients, s.clientKey)
	} else {
		sm.clients[s.clientKey] = newSessions
	}

	if metrics.PrometheusMetrics {
		metrics.TotalRemoveWS.With(
			prometheus.Labels{
				"clientkey": s.clientKey,
			}).Inc()
	}

	s.Close()
}
