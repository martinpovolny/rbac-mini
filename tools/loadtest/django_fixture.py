"""Django-side fixture mirroring the rbac-go load test: tenant 100001 with
100 users in one group holding a cost-management:*:* grant.

Run inside the baseline image (setup steps are in the load-test doc):
  docker run --rm -i --entrypoint python -w /opt/rbac/rbac <env...> rbac-django-baseline manage.py shell < django_fixture.py
"""
from api.models import Tenant
from management.models import Access, Group, Permission, Policy, Principal, Role

tenant, _ = Tenant.objects.get_or_create(
    org_id="100001",
    defaults={"tenant_name": "acct1111111", "account_id": "1111111", "ready": True},
)
public, _ = Tenant.objects.get_or_create(tenant_name="public", defaults={"ready": True})

perm, _ = Permission.objects.get_or_create(permission="cost-management:*:*", tenant=tenant)
role, _ = Role.objects.get_or_create(name="Load Test Admin", tenant=tenant)
access, _ = Access.objects.get_or_create(role=role, permission=perm, tenant=tenant)
group, _ = Group.objects.get_or_create(name="loadtest", tenant=tenant)
policy, _ = Policy.objects.get_or_create(name="loadtest policy", tenant=tenant, group=group)
policy.roles.add(role)

usernames = [f"loaduser-{i}" for i in range(100)]
for username in usernames:
    principal, _ = Principal.objects.get_or_create(username=username, tenant=tenant)
    group.principals.add(principal)

print(f"tenant={tenant.org_id} users={len(usernames)} group={group.name} role={role.name}")
