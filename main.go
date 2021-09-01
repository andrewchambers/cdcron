package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/andrewchambers/go-cdmetrics"
	_ "github.com/andrewchambers/go-cdmetrics/flag"
)

// flags
var (
	printSchedule    = flag.Bool("print-schedule", false, "Print the schedule for the next 24 hours then exit.")
	printScheduleFor = flag.Duration("print-schedule-for", 0*time.Second, "Print the schedule for the specified duration then exit.")
	tab              = flag.String("cron-tab", "/etc/cdcron", "'cdcron' file to load and run.")
)

// metrics
var (
	forwardTimeSkips  = cdmetrics.NewCounter("forward-time-skips")
	backwardTimeSkips = cdmetrics.NewCounter("backward-time-skips")
	overdueCounter    = make(map[string]*cdmetrics.Counter)
	failureCounter    = make(map[string]*cdmetrics.Counter)
	successCounter    = make(map[string]*cdmetrics.Counter)
	durationGauge     = make(map[string]*cdmetrics.Gauge)
	maxrssBytesGauge  = make(map[string]*cdmetrics.Gauge)
	utimeGauge        = make(map[string]*cdmetrics.Gauge)
	stimeGauge        = make(map[string]*cdmetrics.Gauge)
	runningGauge      = make(map[string]*cdmetrics.Gauge)
)

func delayTillNextCheck(fromt time.Time) time.Duration {
	// Schedule for midway in the next minute to be
	// resilient to clock adjustments in both directions.
	return 30*time.Second +
		(time.Duration(60-fromt.Second()) * time.Second) -
		(time.Duration(fromt.Nanosecond()%1000000000) * time.Nanosecond)
}

func onJobExit(jobName string, duration time.Duration, cmd *exec.Cmd, err error) {

	exitStatus := 127
	if err != nil {
		if exiterr, ok := err.(*exec.ExitError); ok {
			if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
				exitStatus = status.ExitStatus()
			}
		}
	} else {
		exitStatus = 0
	}

	log.Printf("job %s finished in %s with exit status %d", jobName, duration, exitStatus)

	runningGauge[jobName].Set(0)

	if exitStatus == 0 {
		successCounter[jobName].Inc()
	} else {
		failureCounter[jobName].Inc()
	}

	durationGauge[jobName].Set(duration.Seconds())

	if rusage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		durationGauge[jobName].Set(duration.Seconds())
		maxrssBytesGauge[jobName].Set(float64(rusage.Maxrss * 1024))
		utimeGauge[jobName].Set(float64(rusage.Utime.Sec) + (float64(rusage.Utime.Usec) / 1000000.0))
		stimeGauge[jobName].Set(float64(rusage.Stime.Sec) + (float64(rusage.Stime.Usec) / 1000000.0))
	}

}

func printScheduleAndExit(jobs []*Job) {
	duration := 24 * time.Hour
	if *printScheduleFor != 0 {
		duration = *printScheduleFor
	}
	simulatedTime := time.Now()
	end := simulatedTime.Add(duration)
	for end.After(simulatedTime) {
		simulatedTime = simulatedTime.Add(delayTillNextCheck(simulatedTime))
		for _, j := range jobs {
			if !j.ShouldRunAt(&simulatedTime) {
				continue
			}
			fmt.Printf("%s - %s\n", simulatedTime.Format("2006/01/02 15:04"), j.Name)
		}
	}
	os.Exit(0)
}

func main() {
	// Our metrics don't change very often.
	cdmetrics.MetricInterval = 30 * time.Second
	cdmetrics.MetricPlugin = "cdcron"
	cdmetrics.MetricPluginInstance = ""

	flag.Parse()

	tabData, err := ioutil.ReadFile(*tab)
	if err != nil {
		log.Fatalf("error reading %q: %s", *tab, err)
	}

	jobs, err := ParseJobs(*tab, string(tabData))
	if err != nil {
		log.Fatalf("%s", err)
	}

	if *printSchedule || *printScheduleFor != 0 {
		printScheduleAndExit(jobs)
	}

	// Init metrics with job names.
	for _, j := range jobs {
		overdueCounter[j.Name] = cdmetrics.NewCounter(j.Name + "-overdue")
		failureCounter[j.Name] = cdmetrics.NewCounter(j.Name + "-failure")
		successCounter[j.Name] = cdmetrics.NewCounter(j.Name + "-success")
		durationGauge[j.Name] = cdmetrics.NewGauge(j.Name + "-duration")
		maxrssBytesGauge[j.Name] = cdmetrics.NewGauge(j.Name + "-maxrss-bytes")
		utimeGauge[j.Name] = cdmetrics.NewGauge(j.Name + "-utime")
		stimeGauge[j.Name] = cdmetrics.NewGauge(j.Name + "-stime")
		runningGauge[j.Name] = cdmetrics.NewGauge(j.Name + "-is-running")
	}

	cdmetrics.Start()

	done := make(chan struct{}, 1)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		log.Printf("shutting down due to signal")
		close(done)
		<-sigs
		log.Fatalf("forcing shutdown due to signal")
	}()

	log.Printf("scheduling %d jobs", len(jobs))

	now := time.Now()
	delay := delayTillNextCheck(now)
	prevCheck := now.Add(delay).Add(-60 * time.Second)

scheduler:
	for {
		now = time.Now()
		delay = delayTillNextCheck(now)
		nextCheck := now.Add(delay)
		actualPrevCheck := nextCheck.Add(-60 * time.Second)

		if actualPrevCheck.Unix() != prevCheck.Unix() {
			if actualPrevCheck.After(prevCheck) {
				log.Printf("forward time jump detected, jobs may have been skipped")
				forwardTimeSkips.Inc()
			} else {
				log.Printf("backward time jump detected, jobs may be run multiple times")
				backwardTimeSkips.Inc()
			}
		}

		select {
		case <-time.After(delay):
		case <-done:
			break scheduler
		}

		for _, j := range jobs {
			if !j.ShouldRunAt(&now) {
				continue
			}
			if j.IsRunning() {
				log.Printf("job %s is overdue", j.Name)
				overdueCounter[j.Name].Inc()
				continue
			}
			log.Printf("starting job %s", j.Name)
			runningGauge[j.Name].Set(1)
			j.Start(onJobExit)
		}

		prevCheck = nextCheck
	}

	for _, j := range jobs {
		if j.IsRunning() {
			log.Printf("waiting for job %s", j.Name)
			j.Wait()
		}
	}
}
