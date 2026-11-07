| System | runs | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms (worst run) | errors |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| rbac-go native, SQLite file DB | 3 | 4495 | 4.3 | 3.7 | 9.7 | 13.8 | 28.2 | 0 |
| rbac-go native, Postgres 16 on localhost | 3 | 2729 | 7.0 | 2.3 | 23.9 | 29.1 | 54.5 | 0 |

Per run:

| System | run | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms | errors | elapsed s |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| rbac-go native, SQLite file DB | native-sqlite-1.json | 4491 | 4.4 | 3.8 | 9.7 | 13.8 | 23.7 | 0 | 1.1 |
| rbac-go native, SQLite file DB | native-sqlite-2.json | 4495 | 4.3 | 3.7 | 9.5 | 13.2 | 23.2 | 0 | 1.1 |
| rbac-go native, SQLite file DB | native-sqlite-3.json | 4505 | 4.3 | 3.7 | 10.0 | 13.8 | 28.2 | 0 | 1.1 |
| rbac-go native, Postgres 16 on localhost | native-pg-1.json | 2826 | 6.6 | 1.9 | 24.7 | 32.7 | 54.5 | 0 | 1.8 |
| rbac-go native, Postgres 16 on localhost | native-pg-2.json | 2577 | 7.3 | 2.4 | 23.9 | 29.1 | 48.4 | 0 | 1.9 |
| rbac-go native, Postgres 16 on localhost | native-pg-3.json | 2729 | 7.0 | 2.3 | 23.7 | 28.1 | 41.9 | 0 | 1.8 |
