package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/gomodule/redigo/redis"
)

// redisTracker provides a shared run tracker backed by Redis so that all pods
// in an HPA-scaled deployment share the same counter for a given runID.
//
// Connection pooling is configured via environment variables:
//   - REDIS_HOST            — redis address (host:port)
//   - REDIS_PASSWORD        — password (optional)
//   - REDIS_TIMEOUT_SECONDS — connection timeout in seconds (optional)
//
type redisTracker struct {
	pool *redis.Pool
}

// newRedisPool creates a *redis.Pool from environment variables.
func newRedisPool() *redis.Pool {
	addr := os.Getenv("REDIS_HOST")
	if addr == "" {
		return nil
	}

	var dialOpts []redis.DialOption

	if pw := os.Getenv("REDIS_PASSWORD"); pw != "" {
		dialOpts = append(dialOpts, redis.DialPassword(pw))
	}

	if ts := os.Getenv("REDIS_TIMEOUT_SECONDS"); ts != "" {
		if secs, err := strconv.Atoi(ts); err == nil {
			dialOpts = append(dialOpts, redis.DialConnectTimeout(time.Duration(secs)*time.Second))
		}
	}

	return &redis.Pool{
		MaxIdle:     3,
		MaxActive:   4,
		Wait:        true,
		IdleTimeout: 240 * time.Second,
		Dial:        func() (redis.Conn, error) { return redis.Dial("tcp", addr, dialOpts...) },
	}
}

// newRedisTracker creates a new Redis-backed tracker.
// Returns nil when REDIS_HOST is not set — callers fall back to in-memory.
func newRedisTracker() *redisTracker {
	pool := newRedisPool()
	if pool == nil {
		log.Println("REDIS_HOST not set, using in-memory run tracker")
		return nil
	}

	// Verify connectivity.
	conn := pool.Get()
	defer conn.Close()
	if _, err := conn.Do("PING"); err != nil {
		log.Printf("redis PING failed, falling back to in-memory tracker: %v", err)
		return nil
	}

	log.Printf("redis tracker connected (host=%s)", os.Getenv("REDIS_HOST"))
	return &redisTracker{pool: pool}
}

// redisKey returns the Redis key for a given runID's counter.
func redisKey(runID string) string {
	return fmt.Sprintf("oops:run:%s:remaining", runID)
}

// redisTTLSecs is the expiry (in seconds) set on every key so stale runs don't leak.
const redisTTLSecs = 6 * 60 * 60 // 6 hours

// Set initialises the counter for runID to total with a TTL.
func (r *redisTracker) Set(runID string, total int) error {
	conn := r.pool.Get()
	defer conn.Close()
	_, err := conn.Do("SETEX", redisKey(runID), redisTTLSecs, total)
	return err
}

// Decr atomically decrements the counter for runID and returns the new value.
// Returns -1 if the key does not exist (i.e. no pod set the tracker for this run).
func (r *redisTracker) Decr(runID string) (int, error) {
	conn := r.pool.Get()
	defer conn.Close()

	key := redisKey(runID)

	exists, err := redis.Bool(conn.Do("EXISTS", key))
	if err != nil {
		return -1, err
	}
	if !exists {
		return -1, nil
	}

	val, err := redis.Int(conn.Do("DECR", key))
	if err != nil {
		return -1, err
	}

	// Refresh TTL so active runs don't expire mid-flight.
	conn.Do("EXPIRE", key, redisTTLSecs)
	return val, nil
}

// Delete removes the key for a completed run.
func (r *redisTracker) Delete(runID string) error {
	conn := r.pool.Get()
	defer conn.Close()
	_, err := conn.Do("DEL", redisKey(runID))
	return err
}
