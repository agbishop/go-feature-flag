---
sidebar_position: 25
description: Inspect your audiences from the command line
---

# 👥 Audience commands

The [`go-feature-flag-cli`](./cli) can inspect the lists used by the [audiences](../configure_flag/audiences). These commands are **read-only**, they never write to the store.

All of them need `--audiences <file>`: a YAML or JSON file with an `audiences:` block, in the same format as the relay proxy configuration.

## `audience exists <list>...`

Tells if the lists exist. Exits with an error if one is missing.

```shell
$ go-feature-flag-cli audience exists --audiences ./relay-proxy.yaml beta ghost
beta: exists
ghost: does not exist
```

## `audience contains <list> <id>...`

Tells if the ids are members of a list. The ids are normalized, hashed and encoded with the settings of the list (`normalize`, `hash`, `encoding`), exactly as GO Feature Flag does, so you can debug a writer that stores ids differently.

```shell
$ go-feature-flag-cli audience contains --audiences ./relay-proxy.yaml emails " Alice@Example.com" bob@example.com
 Alice@Example.com: member
bob@example.com: not member
```

## `audience key <list> <id>`

Prints the key and the member value GO Feature Flag looks up in the store. It works offline (no connection to the store). With `encoding: raw` the member is binary, it is printed as hex.

```shell
$ go-feature-flag-cli audience key --audiences ./relay-proxy.yaml emails " Alice@Example.com"
layout:  set
key:     goff:{emails}
member:  ff8d9819fc0e12bf0d24892e45987e249a28dce836a85cad60e28eaaa8c6d976
```

:::caution
`contains` and `key` print the ids you give them. Do not use them with production ids in shared logs.
:::
