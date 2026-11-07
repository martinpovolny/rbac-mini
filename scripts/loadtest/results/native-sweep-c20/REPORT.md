| System | runs | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms (worst run) | errors |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| rbac-go native, SQLite file DB | 3 | 4388 | 4.5 | 3.9 | 10.1 | 14.2 | 31.8 | 0 |
| rbac-go native, Postgres 16 on localhost | 3 | 12608 | 1.6 | 1.4 | 3.1 | 4.3 | 22.4 | 0 |

Per run:

| System | run | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms | errors | elapsed s |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| rbac-go native, SQLite file DB | native-sqlite-1.json | 4388 | 4.5 | 3.9 | 10.3 | 14.2 | 27.8 | 0 | 6.8 |
| rbac-go native, SQLite file DB | native-sqlite-2.json | 4419 | 4.5 | 3.8 | 10.1 | 14.2 | 31.8 | 0 | 6.8 |
| rbac-go native, SQLite file DB | native-sqlite-3.json | 4386 | 4.5 | 3.9 | 10.1 | 13.9 | 26.9 | 0 | 6.8 |
| rbac-go native, Postgres 16 on localhost | native-pg-1.json | 12596 | 1.6 | 1.4 | 3.1 | 4.3 | 11.4 | 0 | 2.4 |
| rbac-go native, Postgres 16 on localhost | native-pg-2.json | 12766 | 1.6 | 1.4 | 3.1 | 4.3 | 22.4 | 0 | 2.4 |
| rbac-go native, Postgres 16 on localhost | native-pg-3.json | 12608 | 1.6 | 1.4 | 3.2 | 4.3 | 8.8 | 0 | 2.4 |
