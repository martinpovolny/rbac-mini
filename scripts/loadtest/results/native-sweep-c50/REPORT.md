| System | runs | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms (worst run) | errors |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| rbac-go native, SQLite file DB | 3 | 4318 | 11.4 | 9.6 | 26.7 | 37.2 | 72.7 | 0 |
| rbac-go native, Postgres 16 on localhost | 3 | 12594 | 3.8 | 3.3 | 8.4 | 11.7 | 29.6 | 0 |

Per run:

| System | run | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms | errors | elapsed s |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| rbac-go native, SQLite file DB | native-sqlite-1.json | 4285 | 11.5 | 9.7 | 27.1 | 38.4 | 67.3 | 0 | 7.0 |
| rbac-go native, SQLite file DB | native-sqlite-2.json | 4318 | 11.4 | 9.6 | 26.7 | 37.2 | 72.7 | 0 | 6.9 |
| rbac-go native, SQLite file DB | native-sqlite-3.json | 4328 | 11.4 | 9.6 | 26.7 | 36.9 | 72.5 | 0 | 6.9 |
| rbac-go native, Postgres 16 on localhost | native-pg-1.json | 12594 | 3.8 | 3.3 | 8.4 | 11.7 | 24.2 | 0 | 2.4 |
| rbac-go native, Postgres 16 on localhost | native-pg-2.json | 12462 | 3.9 | 3.4 | 8.5 | 12.2 | 29.6 | 0 | 2.4 |
| rbac-go native, Postgres 16 on localhost | native-pg-3.json | 12758 | 3.8 | 3.3 | 8.1 | 11.2 | 27.6 | 0 | 2.4 |
