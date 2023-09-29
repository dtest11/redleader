package redleader

import (
	"context"
	"sync/atomic"

	"github.com/go-redsync/redsync/v4"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/rand"

	"log/slog"

	"k8s.io/apimachinery/pkg/util/wait"

	"time"
)

type LeaderCallbacks struct {
	OnStartedLeading func(context.Context)
	OnStoppedLeading func()
}

type LeaderElectionConfig struct {
	Callbacks             LeaderCallbacks
	LockKey               string
	LeaseDuration         time.Duration // leader lease time
	ExtendPeriodDuration  time.Duration // extend lock period
	NewLockPeriodDuration time.Duration // attempt get lock
}

type LeaderElector struct {
	config       LeaderElectionConfig
	RedSync      *redsync.Redsync
	mutex        *redsync.Mutex
	firstAcquire bool
	isLeader     atomic.Bool
}

// err != nil || ok [return]
func (le *LeaderElector) newLock(ctx context.Context) (bool, error) {
	err := le.mutex.LockContext(ctx)
	if err == nil { // we get lock
		slog.Info("redis Lock acquire success")
		return true, nil // ok
	}
	// failed to acquire lock, need continue retry
	return false, errors.WithMessage(err, "newLock")
}

// err != nil  [return]
func (le *LeaderElector) badExtendLock(ctx context.Context) (bool, error) {
	ok, err := le.mutex.ExtendContext(ctx)
	if ok {
		slog.Info("extend current redis lock expire time")
		return false, nil
	}
	return true, errors.WithMessage(err, "badExtendLock")
}

func NewLeaderElector(conf LeaderElectionConfig, rs *redsync.Redsync) *LeaderElector {
	return &LeaderElector{
		config:  conf,
		RedSync: rs,
		mutex:   rs.NewMutex(conf.LockKey, redsync.WithExpiry(conf.LeaseDuration)),
	}
}

// RunWithPoll use redsync to leader elect
func RunWithPoll(ctx context.Context, lec LeaderElectionConfig, rs *redsync.Redsync) error {
	le := NewLeaderElector(lec, rs)
	le.firstAcquire = true
	for {
		if err := le.Acquire(ctx); err != nil {
			if !errors.Is(err, redsync.ErrFailed) { // we ignore normal error
				slog.Error(err.Error())
			}
			slog.Debug(err.Error())
		}
	}
}

// IsLeader check current server is leader
func (le *LeaderElector) IsLeader() bool {
	return le.isLeader.Load()
}

// Acquire attempt get lock and extend lock
func (le *LeaderElector) Acquire(ctx context.Context) error {
	var period = le.config.NewLockPeriodDuration
	if le.firstAcquire { // first time connect ,we should attempt quick
		period = time.Duration(rand.Intn(3)) * time.Second
		le.firstAcquire = false
	} else {
		random := time.Duration(rand.Intn(5)) * time.Second
		period = period + random // avoid conflict different server at same time
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// block here when we get lock success
	err := wait.PollUntilContextCancel(ctx, period, true, le.newLock)
	if err != nil {
		return err
	}
	defer func() {
		ok, err := le.mutex.Unlock()
		if err != nil {
			slog.Error(err.Error())
			return
		}
		if ok {
			slog.Info("unlock redis lock success")
		}
	}()
	defer le.config.Callbacks.OnStoppedLeading()
	le.isLeader.Store(true)
	defer le.isLeader.Store(false)

	go le.config.Callbacks.OnStartedLeading(ctx)
	// until context Done else will block
	return wait.PollUntilContextCancel(ctx, le.config.ExtendPeriodDuration, false, le.badExtendLock)
}
