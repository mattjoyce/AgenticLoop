package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// bearerAuth is middleware that validates Bearer token authentication.
func (s *Server) bearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			s.writeError(w, http.StatusUnauthorized, "missing Authorization header")
			return
		}

		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			s.writeError(w, http.StatusUnauthorized, "invalid Authorization header format")
			return
		}

		token := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
		if token == "" {
			s.writeError(w, http.StatusUnauthorized, "missing token")
			return
		}

		if !constantTimeEqual(token, s.config.Token) {
			s.writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func constantTimeEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
