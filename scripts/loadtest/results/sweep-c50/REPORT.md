| System | runs | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms (worst run) | errors |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Django + Valkey (policy cache on) | 3 | 890 | 20.5 | 18.9 | 32.6 | 42.7 | 738.7 | 0 |
| Django (policy cache off) | 3 | 325 | 55.2 | 53.5 | 77.7 | 91.2 | 788.5 | 0 |
| rbac-go base (no cache) | 3 | 1029 | 18.9 | 11.6 | 42.0 | 52.5 | 86.8 | 0 |
| rbac-go sweep-c50 | 3 | 5999 | 8.2 | 7.7 | 13.0 | 17.3 | 51.0 | 0 |

Per run:

| System | run | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms | errors | elapsed s |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Django + Valkey (policy cache on) | django-warm-1.json | 890 | 20.5 | 18.9 | 32.6 | 42.7 | 738.7 | 0 | 5.6 |
| Django + Valkey (policy cache on) | django-warm-2.json | 901 | 19.6 | 18.5 | 30.2 | 38.4 | 55.1 | 0 | 5.5 |
| Django + Valkey (policy cache on) | django-warm-3.json | 880 | 20.5 | 19.0 | 33.7 | 42.7 | 71.8 | 0 | 5.7 |
| Django (policy cache off) | django-nocache-1.json | 325 | 55.1 | 53.0 | 77.7 | 91.2 | 788.5 | 0 | 15.4 |
| Django (policy cache off) | django-nocache-2.json | 323 | 56.2 | 54.9 | 79.1 | 91.7 | 782.4 | 0 | 15.5 |
| Django (policy cache off) | django-nocache-3.json | 344 | 55.2 | 53.5 | 77.6 | 90.9 | 157.2 | 0 | 14.5 |
| rbac-go base (no cache) | go-1.json | 1029 | 18.9 | 11.6 | 42.0 | 52.5 | 67.4 | 0 | 4.9 |
| rbac-go base (no cache) | go-2.json | 1007 | 19.4 | 11.8 | 44.4 | 56.3 | 80.9 | 0 | 5.0 |
| rbac-go base (no cache) | go-3.json | 1090 | 18.0 | 10.4 | 41.0 | 52.3 | 86.8 | 0 | 4.6 |
| rbac-go sweep-c50 | go-1.json | 5999 | 8.2 | 7.7 | 13.0 | 17.3 | 47.7 | 0 | 5.0 |
| rbac-go sweep-c50 | go-2.json | 6040 | 8.2 | 7.8 | 12.8 | 16.0 | 28.4 | 0 | 5.0 |
| rbac-go sweep-c50 | go-3.json | 5976 | 8.3 | 7.7 | 13.3 | 19.0 | 51.0 | 0 | 5.0 |
