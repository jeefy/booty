package versions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-co-op/gocron/v2"
)

// Job is an extra periodic task run on the same cron schedule as the
// version checks. Fn gets the scheduler's ctx and should return promptly
// once it is done.
type Job struct {
	Name string
	Fn   func(ctx context.Context)
}

// StartScheduler runs the Flatcar, CoreOS and Bluefin checks, plus any extra
// jobs, on schedule. Every job is a singleton: a tick that arrives while the
// previous run is still executing is dropped. Every run gets ctx, so
// cancelling it stops in-flight checks.
func StartScheduler(ctx context.Context, schedule string, extra ...Job) (gocron.Scheduler, error) {
	jobs := append([]Job{
		{"flatcar", FlatcarVersionCheck},
		{"coreos", CoreOSVersionCheck},
		{"bluefin", BluefinVersionCheck},
	}, extra...)
	s, err := newScheduler(ctx, func() gocron.JobDefinition { return gocron.CronJob(schedule, false) }, schedule, jobs)
	if err != nil {
		return nil, err
	}
	slog.Info("Update scheduler started", "schedule", schedule)
	return s, nil
}

// newScheduler starts jobs, each a singleton on its own definition and
// run with ctx.
func newScheduler(ctx context.Context, def func() gocron.JobDefinition, schedule string, jobs []Job) (gocron.Scheduler, error) {
	s, err := gocron.NewScheduler()
	if err != nil {
		return nil, err
	}
	for _, job := range jobs {
		_, err := s.NewJob(
			def(),
			gocron.NewTask(func() { job.Fn(ctx) }),
			gocron.WithName(job.Name),
			gocron.WithSingletonMode(gocron.LimitModeReschedule),
		)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("scheduling %s check with %q: %w", job.Name, schedule, err), s.Shutdown())
		}
	}
	s.Start()
	return s, nil
}
