---
sidebar_position: 26
description: Redis and Valkey data contract for audiences - layouts, updates, normalization and access.
---

# 👥 Audiences: Redis data contract

:::caution Experimental
Audiences are an experimental feature, see [Audiences](./audiences).
:::

The `redis` provider reads the lists from Redis or Valkey. It is **read-only**: your own tooling writes the lists, GO Feature Flag only checks membership.

**Minimum versions:** Redis 6.2 or Valkey 7.2 (the lookups use `SMISMEMBER`).

## Layouts
The layout is a storage choice, so it is set in the configuration (`layout`), never in the flags. Moving a list from a layout to another needs no flag change.

| Layout           | Keys                                                       | Lookup                         |
|------------------|------------------------------------------------------------|--------------------------------|
| `set` (default)  | `<prefix>{<name>}`, a Redis set of ids                     | `SMISMEMBER`                   |
| `keys`           | `<prefix><name>:<id>`, one key per id, any value           | `EXISTS` per id, pipelined     |

All the lookups of a request go in a single pipeline: one round trip on a single node. On Cluster, each node receives its share in parallel, so the latency is the one of the slowest node.

### Sizing
- **set**: the whole list is in one key, so on a Cluster it lives on one node. The limit is the memory of that node: roughly 1 GB per 10M hex-encoded ids, about half with `encoding: raw`.
- **keys**: no hash tag, the ids spread over all Cluster nodes and the limit is the total memory of the cluster. Each key has its own overhead, so it uses more memory than a set.

## Updating lists
- **Members:** `SADD` / `SREM` for sets, `SET` / `UNLINK` for keys. Changes are visible within the cache `ttl`.
- **Rebuild a whole set without touching flags:** build it at `<prefix>{<name>}:tmp`, then `RENAME` it onto `<prefix>{<name>}`. The swap is atomic and, because both keys share the hash tag, it works on Cluster. Peak memory is twice the list during the rebuild. Lists using the `keys` layout are updated member by member.
- **Switching to another list:** write the new list and change the flags. Remove the old list only after every relay proxy has loaded the new flags.
- Remove large sets with `UNLINK`, never `DEL` (it blocks the server while it frees the memory). Set `lazyfree-lazy-server-del yes` so a replaced set is freed in the background.

```shell
SADD "goff:{beta_testers}:tmp" id1 id2 id3
RENAME "goff:{beta_testers}:tmp" "goff:{beta_testers}"
```

## Normalization and hashing
Writers must store the ids **exactly** as GO Feature Flag looks them up. The steps applied to the attribute value before the lookup are:

1. `normalize`: `none` (default) or `trimLower` (trim spaces and lowercase, recommended for emails so `Bob@x.com` and `bob@x.com` match).
2. `hash`: `none` (default) or `sha256`.
3. `encoding` (with `sha256`): `hex` (default, lowercase hex digest) or `raw` (the 32 bytes digest, half the memory).

For example with `normalize: trimLower`, `hash: sha256`, `encoding: hex`, store the lowercase hex SHA-256 of `trimLower(id)`:

```shell
echo -n "bob@x.com" | sha256sum | cut -d' ' -f1   # then SADD it to the set
```

## Access
GO Feature Flag only reads, give it a read-only user limited to the list prefix:

```text
ACL SETUSER goff on >secret ~goff:* +smismember +exists +scan +ping +info
```

`+scan` is only needed to check at load that `keys` lists exist, and `+info` for an optional server version warning. If the lists share a database with the Redis flag retriever, use a different prefix.

### Redis Cluster
The client discovers the topology with `CLUSTER SLOTS`/`CLUSTER SHARDS`, so the user also needs those subcommands:

```text
ACL SETUSER goff on >secret ~goff:* +smismember +exists +scan +ping +info +cluster|slots +cluster|shards
```

Add `+readonly` too if the client reads from replicas (`ReadOnly`, `RouteByLatency` or `RouteRandomly`), because it sends `READONLY` on those connections.

### Cost of the `SCAN` existence check
For a `set` list the load-time existence check is a single `EXISTS`. For a `keys` list there is no key to test, so it uses `SCAN` with a `MATCH` pattern, which walks the keyspace and is costly on a big database. The effort is bounded (at most 100 `SCAN` calls of `COUNT 1000` per node): if the list is not found within that bound, its existence is reported as **unknown** and no warning is logged. Lookups at evaluation time never use `SCAN`. Drop `+scan` from the ACL to disable this check.

### When the server is down
GO Feature Flag never waits for the store: the provider is initialized in the background, `New` returns immediately and the audience terms are unknown (or non-members with `failMode: empty`) until the store answers. A failed first connection is not final: the client keeps reconnecting, a background ping retries with a backoff, and the provider becomes ready as soon as a command succeeds. Store failures are logged as a warning (first occurrence, then at most once per minute, without any list name or id), and as an info when the store is back.
