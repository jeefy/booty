package versions

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-co-op/gocron/v2"
)

// Job is an extra periodic task run on the same cron schedule as the
// version checks.
type Job struct {
	Name string
	Fn   func()
}

// StartScheduler runs the Flatcar, CoreOS and Bluefin checks, plus any extra
// jobs, on schedule. Every job is a singleton: a tick that arrives while the
// previous run is still executing is dropped.
func StartScheduler(schedule string, extra ...Job) (gocron.Scheduler, error) {
	jobs := append([]Job{
		{"flatcar", FlatcarVersionCheck},
		{"coreos", CoreOSVersionCheck},
		{"bluefin", BluefinVersionCheck},
	}, extra...)
	s, err := newScheduler(func() gocron.JobDefinition { return gocron.CronJob(schedule, false) }, schedule, jobs)
	if err != nil {
		return nil, err
	}
	slog.Info("Update scheduler started", "schedule", schedule)
	return s, nil
}

// newScheduler starts jobs, each a singleton on its own definition.
func newScheduler(def func() gocron.JobDefinition, schedule string, jobs []Job) (gocron.Scheduler, error) {
	s, err := gocron.NewScheduler()
	if err != nil {
		return nil, err
	}
	for _, job := range jobs {
		_, err := s.NewJob(
			def(),
			gocron.NewTask(job.Fn),
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
