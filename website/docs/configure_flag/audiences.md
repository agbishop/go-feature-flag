---
sidebar_position: 25
description: Target or exclude very large lists of users stored outside of the flag file, checked at evaluation time.
---

# 👥 Audiences

:::caution Experimental
Audiences are an experimental feature. The syntax, the configuration and the data contract may change in a future release.
:::

## Overview
An **audience** is a named set of targeting keys (user ids, emails, ...) that lives **outside** of the flag file and is checked when a flag is evaluated.
It lets you target or exclude millions of users without growing the flag file.

Membership is stored in an external store that **you** populate with your own tooling, GO Feature Flag only reads it.
At each evaluation it looks up only the ids of the current request, so its memory usage stays flat however large the lists are.

Today the only supported store is [Redis (or Valkey)](./audiences-redis). Other stores (PostgreSQL, DynamoDB, ...) may follow.

:::info
Audiences are configured in the GO Feature Flag configuration (relay proxy or Go module), never in the flag file. See [Configure audiences](../relay-proxy/configure-relay-proxy#audiences) and [the Redis data contract](./audiences-redis).
:::

## Flag syntax
Audiences are a term of the rule `query`.
`<attribute> in audience("name", ...)` is true when the value of the attribute is a member of **any** of the listed audiences.

```yaml title="flag.goff.yaml"
my-flag:
  variations:
    on: true
    off: false
  defaultRule:
    variation: off
  targeting:
    - name: beta-not-churned
      query: >
        targetingKey in audience("beta_testers")
        and not (targetingKey in audience("churned"))
        and country eq "US"
      variation: on
```

| Intent                   | Query                                                                          |
|--------------------------|--------------------------------------------------------------------------------|
| Include (any of several) | `targetingKey in audience("a", "b")`                                           |
| Exclude only             | `not (targetingKey in audience("c"))`, matches everyone who is not in `c`      |
| Include and exclude      | `targetingKey in audience("a") and not (targetingKey in audience("c"))`        |
| With other conditions    | `targetingKey in audience("a") and country eq "US"`                            |
| Another attribute        | `email in audience("vip_emails")`                                              |

Rules:
- Audience names must be string literals matching `[a-z0-9_]{1,64}`. `audience(someVariable)` is rejected.
- The attribute is chosen in the query, any string attribute of the evaluation context can be checked against a list.
- `audience()` is not allowed in the `defaultRule`.
- `x not in audience("a")` is **not supported**, use `not (x in audience("a"))`.
- Queries containing the reserved prefix `goff_aud_` are rejected. GO Feature Flag also strips any context attribute with this prefix, so membership cannot be spoofed by a client.

### JsonLogic
With the [JsonLogic](./target-with-flags#jsonlogic-format) query format, use the `inAudience` operation:

```json
{"and": [
  {"inAudience": [{"var": "targetingKey"}, "beta_testers"]},
  {"!": {"inAudience": [{"var": "targetingKey"}, "churned"]}}
]}
```

`inAudience` is **not allowed inside iterator operations** (`some`, `all`, `none`, `map`, `filter`, `reduce`).

## Failure handling
An audience term is **unknown** when:
- the lookup fails (store error or `lookupTimeout`),
- the evaluation context has no such attribute, or it is not a string,
- audiences are not configured (for example in-process or WASM evaluation).

Every audience listed in a query needs a membership answer, a list that does not exist in the store is **not** an error: it matches nobody.

By default (`failMode: unknown`) an unknown term is not treated as a member or a non-member. The rule is skipped, **only if the unknown terms would change the outcome of the rule** (GO Feature Flag checks the combinations of up to 4 unknown terms). For example `country eq "FR" and targetingKey in audience("a")` is still false for a US user even if the store is down. When the outcome depends on the unknown terms, the rule's `onError` decides:

| `onError`        | Behavior                                                                                                                       |
|------------------|--------------------------------------------------------------------------------------------------------------------------------|
| `skip` (default) | The rule does not match, evaluation moves to the next rule or the default rule.                                                |
| `match`          | Unknown terms are considered as members and the query is evaluated. Use it for deny rules that must keep blocking during an outage. |

```yaml
- name: blocklist
  query: targetingKey in audience("blocklist")
  onError: match
  variation: "off"
```

:::note
`onError: match` also applies when audiences are not configured (WASM, in-process providers).
:::

`failMode` (global or per list, in the configuration) can change how failures are seen before the rule is evaluated:

| `failMode`          | A failed lookup is treated as                                                                 |
|---------------------|-----------------------------------------------------------------------------------------------|
| `unknown` (default) | Unknown, `onError` applies when the outcome depends on it.                                    |
| `empty`             | Not a member (as other vendors do). Users of an exclude list get through during an outage.    |

The evaluation reason stays `TARGETING_MATCH` or `DEFAULT`, never `ERROR`. The readiness of the relay proxy never depends on the store.

## Caching and metadata
- Lookup results are kept in an LRU cache (`cache.size`, `cache.ttl`), a membership change is visible within the TTL. Errors are never cached.
- Flags using audiences are **non-cacheable**: they are not stored in the internal evaluation cache and the OFREP responses tell clients not to cache them, so client-side providers refetch them on every refresh.
- Flags that evaluated an audience carry `gofeatureflag_audienceStatus` in their `flagMetadata`:

| Value            | Meaning                                                                      |
|------------------|------------------------------------------------------------------------------|
| `HEALTHY`        | Every lookup was answered by the store.                                      |
| `STORE_ERROR`    | At least one lookup failed or was missing.                                   |
| `NOT_CONFIGURED` | No membership data (audiences not configured, in-process or WASM provider).  |

When several audiences are used, the worst status wins.

## Limitations
- Only Redis / Valkey is available as a store.
- In-process and WASM OpenFeature providers have no access to the store: their audience terms are unknown, the rules using them are skipped (or matched with `onError: match`) and the flags report `NOT_CONFIGURED`. Use the relay proxy (OFREP) to evaluate audiences.
- Audience names are static, lists cannot be chosen from the evaluation context.
- `audience()` is new query syntax: older GO Feature Flag versions, linters and editors reject flag files that use it.
- Hashing (`sha256`) is obfuscation, not secrecy.

## OpenFeature compatibility
Nothing changes in OpenFeature: the SDKs send the `targetingKey` and attributes, and receive a value, a reason and `flagMetadata`. No new error code is introduced.
Single and bulk OFREP evaluations are supported (bulk resolves the lookups once per request). In the Go module, see `RawVariationWithContext` and `GetFlagStatesWithContext` to bound the lookups with a `context.Context`.
