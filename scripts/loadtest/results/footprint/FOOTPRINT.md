## Serving path under load

| Configuration | containers on the path | samples | mean CPU (cores) | peak CPU (cores) | peak memory (MiB) | rps | CPU ms per request |
|---|---|---:|---:|---:|---:|---:|---:|
| rbac-go (no cache) | rbac-go-srv, rbac-baseline-db | 19 | 2.76 | 2.94 | 407 | 5382 | 0.51 |
| Python: Django, policy cache off | rbac-django-srv, rbac-baseline-db | 42 | 5.00 | 6.49 | 2762 | 310 | 16.12 |
| Python + Valkey: Django, policy cache on | rbac-django-srv, rbac-baseline-db, rbac-baseline-redis | 35 | 4.76 | 7.74 | 2552 | 876 | 5.43 |

## Load generator

The load tool runs on the host, outside the serving path. Its CPU time and peak memory come from `/usr/bin/time -l` per run.

| Configuration | runs | wall s | mean CPU (cores) | peak memory (MiB) |
|---|---:|---:|---:|---:|
| rbac-go (no cache) | 3 | 18.9 | 0.61 | 41 |
| Python: Django, policy cache off | 3 | 42.3 | 0.06 | 32 |
| Python + Valkey: Django, policy cache on | 3 | 36.2 | 0.13 | 33 |

## Per container

Memory before is the first sample taken after the stack was up, before any load in that configuration.

| Configuration | container | memory before (MiB) | memory peak (MiB) | mean CPU (%) | peak CPU (%) |
|---|---|---:|---:|---:|---:|
| rbac-go (no cache) | rbac-go-srv | 7 | 20 | 136.8 | 146.4 |
| rbac-go (no cache) | rbac-django-srv | 2146 | 2162 | 0.2 | 0.4 |
| rbac-go (no cache) | rbac-baseline-db | 322 | 387 | 139.6 | 148.5 |
| rbac-go (no cache) | rbac-baseline-redis | 14 | 14 | 0.2 | 0.3 |
| Python: Django, policy cache off | rbac-go-srv | 19 | 21 | 0.0 | 0.3 |
| Python: Django, policy cache off | rbac-django-srv | 304 | 2296 | 400.7 | 528.5 |
| Python: Django, policy cache off | rbac-baseline-db | 123 | 466 | 99.3 | 137.7 |
| Python: Django, policy cache off | rbac-baseline-redis | 14 | 14 | 8.4 | 12.3 |
| Python + Valkey: Django, policy cache on | rbac-go-srv | 21 | 21 | 0.0 | 0.4 |
| Python + Valkey: Django, policy cache on | rbac-django-srv | 304 | 2146 | 430.4 | 719.2 |
| Python + Valkey: Django, policy cache on | rbac-baseline-db | 133 | 394 | 30.1 | 39.3 |
| Python + Valkey: Django, policy cache on | rbac-baseline-redis | 14 | 14 | 15.3 | 18.6 |

Processes at the end of the run:

```
rbac-go processes:     1
django processes:      22
```
