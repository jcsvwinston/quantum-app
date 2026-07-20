package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestSessionsInRedis exercises session_store: redis end to end: login sets a
// session cookie whose token is a live key in Redis (nucleus:sessions:<token>),
// the session authenticates /api/me, and logout destroys both the session and
// the Redis key.
func TestSessionsInRedis(t *testing.T) {
	base := appURL(t)
	c := client(t)

	rdb := redis.NewClient(&redis.Options{Addr: envOr("QA_REDIS_ADDR", "127.0.0.1:56379")})
	defer rdb.Close()
	ctx := context.Background()

	// Wrong password never authenticates.
	if status := doJSON(t, c, http.MethodPost, base+"/api/login", map[string]string{
		"email": envOr("QA_OPS_EMAIL", "ops@warehouse.local"), "password": "wrong",
	}, nil); status != http.StatusUnauthorized {
		t.Fatalf("bad-password login: status %d (want 401)", status)
	}

	// No session yet: /api/me is 401.
	if status := doJSON(t, c, http.MethodGet, base+"/api/me", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /api/me: status %d (want 401)", status)
	}

	token := login(t, c, base)
	redisKey := envOr("QA_SESSION_PREFIX", "nucleus:sessions:") + token

	// The session key is real and lives in Redis.
	var exists int64
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		exists, err = rdb.Exists(ctx, redisKey).Result()
		if err == nil && exists == 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("redis EXISTS: %v", err)
	}
	if exists != 1 {
		t.Fatalf("session key %q not found in Redis after login", redisKey)
	}

	// The session authenticates the API.
	var me struct {
		Email string `json:"email"`
	}
	if status := doJSON(t, c, http.MethodGet, base+"/api/me", nil, &me); status != http.StatusOK {
		t.Fatalf("/api/me with session: status %d (want 200)", status)
	}
	if me.Email != envOr("QA_OPS_EMAIL", "ops@warehouse.local") {
		t.Fatalf("/api/me email = %q", me.Email)
	}

	// Logout destroys the session server-side: the Redis key is gone and the
	// API is unauthenticated again.
	if status := doJSON(t, c, http.MethodPost, base+"/api/logout", nil, nil); status != http.StatusNoContent {
		t.Fatalf("logout: status %d (want 204)", status)
	}
	exists, err = rdb.Exists(ctx, redisKey).Result()
	if err != nil {
		t.Fatalf("redis EXISTS after logout: %v", err)
	}
	if exists != 0 {
		t.Fatalf("session key %q still present in Redis after logout", redisKey)
	}
	if status := doJSON(t, c, http.MethodGet, base+"/api/me", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("/api/me after logout: status %d (want 401)", status)
	}
}
