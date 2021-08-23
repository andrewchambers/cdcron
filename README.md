# cdcron

`cdcron` is cron service which can also export prometheus metrics.

Because `cdcron` uses a simple and robust time keeping algorithm,
it is not suited for thousands of jobs,
but in exchange is able to detect time keeping anomalies which are exported as a metric.

How `cdcron` handles some edge cases:

- If a job is overdue, `cdcron` logs it, but does not run it.
- If time jumps forward more than 30 seconds, `cdcron` may miss jobs
  but attempts to log them and export time anomaly metrics.
- If time jumps backwards more than 30 seconds, `cdcron` may run jobs
  multiple times, but attempts to log them and export time anomaly metrics.

## Example

/etc/cdcron:
```
# All fields are mandatory
#         ┌───────────── minute (0 - 59)
#         │ ┌───────────── hour (0 - 23)
#         │ │ ┌───────────── day of the month (1 - 31)
#         │ │ │ ┌───────────── month (1 - 12, jan-dec)
#         │ │ │ │ ┌───────────── day of the week (0 - 6, mon-sun) 
#         │ │ │ │ │
#         │ │ │ │ │
job-label 0 * * * * echo 'An hour has passed'
# Repeat and range syntax
job2 */10 * * * * echo 'Every 10 minutes'
job3 0-5  * * * * echo 'First 5 minutes of each hour'
```

Run collectd:

```
$ collectd -C ./example/collectd.conf -f 
```

Run cdcron:
```
$ cdcron -metrics-mode unencrypted -cron-tab ./example/example.crontab
```

## Example of exported metrics

The table:
```
job1 0/2 * * * * echo hello
job2 1/2 * * * * sleep 300
```

produces the following exported metrics:
```
black.go-cdcron.gauge-job1-duration 0.010488727 1629722407
black.go-cdcron.counter-job1-success 1 1629722407
black.go-cdcron.gauge-job1-utime 0.003877 1629722407
black.go-cdcron.counter-job2-failure 0 1629722407
black.go-cdcron.counter-job2-success 0 1629722407
black.go-cdcron.gauge-job2-duration 0 1629722407
black.go-cdcron.gauge-job2-maxrss-bytes 0 1629722407
...
```