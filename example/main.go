package main

import (
	"context"
	"github.com/go-co-op/gocron"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/dtest11/redleader"
	"github.com/go-redsync/redsync/v4"
	redsyncredis "github.com/go-redsync/redsync/v4/redis"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/redis/go-redis/v9"
	"k8s.io/apimachinery/pkg/util/waitgroup"
)

func NewRs() *redsync.Redsync {
	redisClients := []*redis.Client{
		redis.NewClient(&redis.Options{
			Addr: "localhost:6379",
		}),
		redis.NewClient(&redis.Options{
			Addr: "localhost:6380",
		}),
		redis.NewClient(&redis.Options{
			Addr: "localhost:6381",
		}),
	}

	// Convert the Redis clients to the redsync.Client type
	var pools []redsyncredis.Pool
	for _, client := range redisClients {
		p := goredis.NewPool(client)
		pools = append(pools, p)
	}

	rs := redsync.New(pools...)
	return rs
}

type Task struct {
	cron  *gocron.Scheduler
	mutex sync.RWMutex
}

func (t *Task) Start() {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	slog.Info("task func is add")

	job, err := t.cron.Every(5).Seconds().Do(func() {
		slog.Info("task in run on leader server")
	})
	if err != nil {
		panic(err)
	}
	job.Tag("tag")
}

func (t *Task) Stop() {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	slog.Info("remove task ")
	err := t.cron.RemoveByTag("tag")
	if err != nil {
		panic(err)
	}
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	s := gocron.NewScheduler(time.Local)
	s.StartAsync()
	defer s.Stop()

	task := &Task{cron: s}

	conf := redleader.LeaderElectionConfig{LockKey: "my-leader-lock",
		LeaseDuration:         1 * time.Minute,
		ExtendPeriodDuration:  40 * time.Second,
		NewLockPeriodDuration: 1 * time.Minute,
		Callbacks: redleader.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				task.Start()
			},
			OnStoppedLeading: func() {
				task.Stop()
			},
		}}
	var w waitgroup.SafeWaitGroup
	_ = w.Add(1)
	go func() {
		defer w.Done()
		if err := redleader.RunWithPoll(ctx, conf, NewRs()); err != nil {
			panic(err)
		}
	}()

	<-ctx.Done()
	stop()
	w.Wait()
}
