| System | runs | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms (worst run) | errors |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| rbac-go native, SQLite file DB | 3 | 4276 | 22.7 | 19.0 | 54.1 | 75.8 | 175.6 | 0 |
| rbac-go native, Postgres 16 on localhost | 3 | 12235 | 7.3 | 6.1 | 16.3 | 23.1 | 72.7 | 0 |

Per run:

| System | run | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms | errors | elapsed s |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| rbac-go native, SQLite file DB | native-sqlite-1.json | 4297 | 22.6 | 19.0 | 53.6 | 75.3 | 152.6 | 0 | 7.0 |
| rbac-go native, SQLite file DB | native-sqlite-2.json | 4276 | 22.7 | 19.0 | 54.4 | 75.8 | 175.6 | 0 | 7.0 |
| rbac-go native, SQLite file DB | native-sqlite-3.json | 4261 | 22.9 | 19.2 | 54.1 | 77.1 | 150.7 | 0 | 7.0 |
| rbac-go native, Postgres 16 on localhost | native-pg-1.json | 12235 | 7.5 | 6.2 | 16.8 | 23.7 | 68.5 | 0 | 2.5 |
| rbac-go native, Postgres 16 on localhost | native-pg-2.json | 12233 | 7.3 | 6.1 | 16.3 | 23.1 | 72.7 | 0 | 2.5 |
| rbac-go native, Postgres 16 on localhost | native-pg-3.json | 12726 | 7.2 | 6.0 | 15.8 | 22.1 | 59.2 | 0 | 2.4 |
