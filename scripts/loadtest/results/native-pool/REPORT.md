| System | runs | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms (worst run) | errors |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| rbac-go native, SQLite file DB | 3 | 4393 | 4.4 | 3.8 | 10.1 | 13.7 | 33.3 | 0 |
| rbac-go native, Postgres 16 on localhost | 3 | 12516 | 1.6 | 1.4 | 3.2 | 4.5 | 10.4 | 0 |

Per run:

| System | run | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms | errors | elapsed s |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| rbac-go native, SQLite file DB | native-sqlite-1.json | 4393 | 4.4 | 3.7 | 10.1 | 14.2 | 21.3 | 0 | 1.1 |
| rbac-go native, SQLite file DB | native-sqlite-2.json | 4353 | 4.5 | 3.8 | 10.2 | 13.7 | 33.3 | 0 | 1.1 |
| rbac-go native, SQLite file DB | native-sqlite-3.json | 4427 | 4.4 | 3.8 | 10.0 | 13.5 | 29.5 | 0 | 1.1 |
| rbac-go native, Postgres 16 on localhost | native-pg-1.json | 11909 | 1.6 | 1.4 | 3.4 | 4.8 | 10.4 | 0 | 0.4 |
| rbac-go native, Postgres 16 on localhost | native-pg-2.json | 12516 | 1.6 | 1.4 | 3.2 | 4.5 | 9.8 | 0 | 0.4 |
| rbac-go native, Postgres 16 on localhost | native-pg-3.json | 12811 | 1.5 | 1.4 | 3.1 | 4.1 | 8.9 | 0 | 0.4 |
