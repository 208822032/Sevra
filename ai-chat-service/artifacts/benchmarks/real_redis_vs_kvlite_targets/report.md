# Real Redis vs KVLite Benchmark Report

Generated at: 2026-03-31T04:48:34Z

Run ID: 20260331T034207Z-target-01

Repeat runs: 5

Warmup requests per run: 5000

## Scenario Results

| workload | backend | run_id | qps | p50(ms) | p95(ms) | p99(ms) | errors | notes |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | --- |
| hot_sessions | redis | 20260331T034207Z-target-01 | 2642.10 | 0.498 | 0.973 | 1.165 | 0 | 热点会话 |
| hot_sessions | kvlite | 20260331T034207Z-target-01 | 2491.23 | 0.511 | 1.032 | 1.260 | 1 | 热点会话 |
| write_heavy | kvlite | 20260331T034207Z-target-01 | 2003.23 | 0.495 | 0.613 | 0.751 | 0 | 写多场景 |
| write_heavy | redis | 20260331T034207Z-target-01 | 1982.54 | 0.502 | 0.626 | 0.765 | 0 | 写多场景 |
| read_heavy | redis | 20260331T034207Z-target-01 | 2900.97 | 0.004 | 1.426 | 1.648 | 0 | 读多场景 |
| read_heavy | kvlite | 20260331T034207Z-target-01 | 2671.63 | 0.004 | 1.500 | 1.751 | 0 | 读多场景 |
| hot_sessions | redis | 20260331T034207Z-target-02 | 2458.77 | 0.502 | 1.063 | 1.483 | 0 | 热点会话 |
| hot_sessions | kvlite | 20260331T034207Z-target-02 | 2418.51 | 0.522 | 1.044 | 1.416 | 0 | 热点会话 |
| write_heavy | kvlite | 20260331T034207Z-target-02 | 1835.29 | 0.543 | 0.655 | 0.789 | 0 | 写多场景 |
| write_heavy | redis | 20260331T034207Z-target-02 | 1604.94 | 0.582 | 0.836 | 1.237 | 0 | 写多场景 |
| read_heavy | redis | 20260331T034207Z-target-02 | 2780.69 | 0.004 | 1.485 | 1.699 | 0 | 读多场景 |
| read_heavy | kvlite | 20260331T034207Z-target-02 | 2700.95 | 0.004 | 1.486 | 1.695 | 0 | 读多场景 |
| hot_sessions | redis | 20260331T034207Z-target-03 | 2780.41 | 0.464 | 0.924 | 1.173 | 0 | 热点会话 |
| hot_sessions | kvlite | 20260331T034207Z-target-03 | 2408.51 | 0.515 | 1.074 | 1.402 | 0 | 热点会话 |
| write_heavy | kvlite | 20260331T034207Z-target-03 | 1574.60 | 0.615 | 0.828 | 1.040 | 0 | 写多场景 |
| write_heavy | redis | 20260331T034207Z-target-03 | 1523.57 | 0.639 | 0.885 | 1.075 | 0 | 写多场景 |
| read_heavy | redis | 20260331T034207Z-target-03 | 2412.39 | 0.005 | 1.696 | 2.151 | 0 | 读多场景 |
| read_heavy | kvlite | 20260331T034207Z-target-03 | 2414.27 | 0.005 | 1.650 | 1.964 | 0 | 读多场景 |
| hot_sessions | redis | 20260331T034207Z-target-04 | 2401.81 | 0.544 | 1.070 | 1.288 | 0 | 热点会话 |
| hot_sessions | kvlite | 20260331T034207Z-target-04 | 2331.69 | 0.559 | 1.083 | 1.290 | 0 | 热点会话 |
| write_heavy | kvlite | 20260331T034207Z-target-04 | 1580.11 | 0.621 | 0.816 | 0.986 | 0 | 写多场景 |
| write_heavy | redis | 20260331T034207Z-target-04 | 1609.25 | 0.605 | 0.805 | 0.994 | 0 | 写多场景 |
| read_heavy | redis | 20260331T034207Z-target-04 | 2486.73 | 0.005 | 1.656 | 1.969 | 0 | 读多场景 |
| read_heavy | kvlite | 20260331T034207Z-target-04 | 2696.15 | 0.004 | 1.495 | 1.689 | 0 | 读多场景 |
| hot_sessions | redis | 20260331T034207Z-target-05 | 2654.81 | 0.504 | 0.967 | 1.116 | 0 | 热点会话 |
| hot_sessions | kvlite | 20260331T034207Z-target-05 | 2870.26 | 0.463 | 0.880 | 1.009 | 0 | 热点会话 |
| write_heavy | kvlite | 20260331T034207Z-target-05 | 1931.72 | 0.516 | 0.626 | 0.765 | 0 | 写多场景 |
| write_heavy | redis | 20260331T034207Z-target-05 | 1993.10 | 0.501 | 0.604 | 0.738 | 0 | 写多场景 |
| read_heavy | redis | 20260331T034207Z-target-05 | 2992.54 | 0.004 | 1.384 | 1.532 | 0 | 读多场景 |
| read_heavy | kvlite | 20260331T034207Z-target-05 | 2867.96 | 0.004 | 1.398 | 1.564 | 0 | 读多场景 |

## Aggregate Summary

| workload | backend | samples | qps mean | qps median | qps stddev | qps cv | p99 mean | p99 median | p99 stddev | elapsed mean(ms) | elapsed median(ms) | elapsed stddev(ms) | errors |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| hot_sessions | kvlite | 5 | 2504.04 | 2418.51 | 212.38 | 0.085 | 1.276 | 1.290 | 0.164 | 40147.07 | 41347.73 | 3122.42 | 1 |
| hot_sessions | redis | 5 | 2587.58 | 2642.10 | 154.74 | 0.060 | 1.245 | 1.173 | 0.147 | 38757.64 | 37848.70 | 2331.80 | 0 |
| read_heavy | kvlite | 5 | 2670.19 | 2696.15 | 162.96 | 0.061 | 1.733 | 1.695 | 0.147 | 37566.50 | 37089.87 | 2380.86 | 0 |
| read_heavy | redis | 5 | 2714.67 | 2780.69 | 254.76 | 0.094 | 1.800 | 1.699 | 0.254 | 37103.20 | 35962.26 | 3550.09 | 0 |
| write_heavy | kvlite | 5 | 1784.99 | 1835.29 | 198.70 | 0.111 | 0.866 | 0.789 | 0.136 | 56593.78 | 54487.25 | 6420.45 | 0 |
| write_heavy | redis | 5 | 1742.68 | 1609.25 | 226.40 | 0.130 | 0.962 | 0.994 | 0.211 | 58139.43 | 62140.84 | 7285.45 | 0 |

## Hot Path Stats

| workload | backend | run_id | hot hit ratio | meta hit ratio | window hit ratio | hot read p99(ms) | write->read p99(ms) |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| hot_sessions | redis | 20260331T034207Z-target-01 | 0.95 | 1.00 | 0.70 | 1.202 | 246.072 |
| hot_sessions | kvlite | 20260331T034207Z-target-01 | 0.95 | 1.00 | 0.70 | 1.306 | 262.278 |
| write_heavy | kvlite | 20260331T034207Z-target-01 | 0.97 | 1.00 | 0.20 | 0.645 | 83.828 |
| write_heavy | redis | 20260331T034207Z-target-01 | 0.97 | 1.00 | 0.20 | 0.660 | 83.342 |
| read_heavy | redis | 20260331T034207Z-target-01 | 0.96 | 1.00 | 0.80 | 1.675 | 267.890 |
| read_heavy | kvlite | 20260331T034207Z-target-01 | 0.96 | 1.00 | 0.80 | 1.787 | 292.303 |
| hot_sessions | redis | 20260331T034207Z-target-02 | 0.95 | 1.00 | 0.70 | 1.551 | 266.505 |
| hot_sessions | kvlite | 20260331T034207Z-target-02 | 0.95 | 1.00 | 0.70 | 1.451 | 266.176 |
| write_heavy | kvlite | 20260331T034207Z-target-02 | 0.97 | 1.00 | 0.20 | 0.672 | 90.175 |
| write_heavy | redis | 20260331T034207Z-target-02 | 0.97 | 1.00 | 0.20 | 0.985 | 105.500 |
| read_heavy | redis | 20260331T034207Z-target-02 | 0.96 | 1.00 | 0.80 | 1.734 | 277.396 |
| read_heavy | kvlite | 20260331T034207Z-target-02 | 0.96 | 1.00 | 0.80 | 1.730 | 286.899 |
| hot_sessions | redis | 20260331T034207Z-target-03 | 0.95 | 1.00 | 0.70 | 1.221 | 232.085 |
| hot_sessions | kvlite | 20260331T034207Z-target-03 | 0.95 | 1.00 | 0.70 | 1.453 | 266.269 |
| write_heavy | kvlite | 20260331T034207Z-target-03 | 0.97 | 1.00 | 0.20 | 0.859 | 106.223 |
| write_heavy | redis | 20260331T034207Z-target-03 | 0.97 | 1.00 | 0.20 | 0.908 | 109.619 |
| read_heavy | redis | 20260331T034207Z-target-03 | 0.96 | 1.00 | 0.80 | 2.207 | 323.321 |
| read_heavy | kvlite | 20260331T034207Z-target-03 | 0.96 | 1.00 | 0.80 | 2.004 | 322.939 |
| hot_sessions | redis | 20260331T034207Z-target-04 | 0.95 | 1.00 | 0.70 | 1.329 | 269.850 |
| hot_sessions | kvlite | 20260331T034207Z-target-04 | 0.95 | 1.00 | 0.70 | 1.340 | 278.076 |
| write_heavy | kvlite | 20260331T034207Z-target-04 | 0.97 | 1.00 | 0.20 | 0.832 | 104.858 |
| write_heavy | redis | 20260331T034207Z-target-04 | 0.97 | 1.00 | 0.20 | 0.833 | 104.170 |
| read_heavy | redis | 20260331T034207Z-target-04 | 0.96 | 1.00 | 0.80 | 2.013 | 310.880 |
| read_heavy | kvlite | 20260331T034207Z-target-04 | 0.96 | 1.00 | 0.80 | 1.720 | 286.145 |
| hot_sessions | redis | 20260331T034207Z-target-05 | 0.95 | 1.00 | 0.70 | 1.160 | 244.093 |
| hot_sessions | kvlite | 20260331T034207Z-target-05 | 0.95 | 1.00 | 0.70 | 1.042 | 226.777 |
| write_heavy | kvlite | 20260331T034207Z-target-05 | 0.97 | 1.00 | 0.20 | 0.653 | 86.183 |
| write_heavy | redis | 20260331T034207Z-target-05 | 0.97 | 1.00 | 0.20 | 0.632 | 83.218 |
| read_heavy | redis | 20260331T034207Z-target-05 | 0.96 | 1.00 | 0.80 | 1.552 | 257.479 |
| read_heavy | kvlite | 20260331T034207Z-target-05 | 0.96 | 1.00 | 0.80 | 1.592 | 269.295 |

## Redis vs KVLite Comparison

| workload | redis qps(median) | kvlite qps(median) | redis p99(median) | kvlite p99(median) | samples | winner | conclusion |
| --- | ---: | ---: | ---: | ---: | ---: | --- | --- |
| hot_sessions | 2642.10 | 2418.51 | 1.173 | 1.290 | 5 | redis | redis baseline favored |
| read_heavy | 2780.69 | 2696.15 | 1.699 | 1.695 | 5 | redis | redis baseline throughput better |
| write_heavy | 1609.25 | 1835.29 | 0.994 | 0.789 | 5 | kvlite | kvlite base favored |

## KVLite Better Scenarios

- write_heavy

## Redis Better Scenarios

- hot_sessions
- read_heavy

## Final Conclusion

Redis remains stronger in 2 of 3 comparable workloads; KVLite_Base is still valuable for light-path scenarios, but the benchmark does not support a blanket throughput claim.

## Environment

- Redis namespace: aggregate:redis
- KVLite namespace: aggregate:kvlite
- Repeat runs: 5
- Warmup requests: 5000
- Seed base: 1774853392387908100
- Sample count: 30
- Total requests: 3000000
- Total turns: 3000000
