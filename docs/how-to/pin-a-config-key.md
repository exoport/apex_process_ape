# How to pin a config key the framework falls back on (`ape config pin`)

Some `_apex/config.yaml` keys have a fallback: when the project does not set the key, the
framework's skills resolve a value of their own. `ape config pin <key>` writes that resolved value
into the project's config, so every reader sees the same value and a later change to the fallback
cannot silently move it.

Today it pins one key, `evidence_folder`.

## Where it matters

`evidence_folder` is an APEX v0 key: it names the folder v0 kept its evidence in. An APEX v1
project does not use it. It matters at one point of a v1 project's life: when
[`ape project lift`](lift-a-v0-project.md) lifts a v0 project, it treats every file under the
configured `evidence_folder` (and under any folder named `evidence`) as a record, so it never
reports one as a hashed file it edited. The APEX v0 framework's migration `v0.22.0_seq-01` pins the
key with this command, so the lift and the v0 skills read the same folder.

In a project made with `ape project init`, there is nothing to pin.

## Check whether the key is set

```bash
ape config pin evidence_folder --check
```

It writes nothing and answers in its exit code: 0 when the key is set, 1 when it is not. A missing
config still exits 4, and a malformed one exits 2: those mean the check itself could not run, which
a migration runner reads as a failed check, never as "not applied".

## Pin it

```bash
ape config pin evidence_folder
```

It appends the key to `_apex/config.yaml` with the value the project's skills were already
resolving: an existing `{governance_folder}/evidence` where the project keeps its evidence there,
else the literal `evidence`. So no install moves its evidence. It does nothing when the base config
or the `config.local.yaml` overlay already sets the key.

## Related

- [`_apex/config.yaml`](../reference/config-yaml.md): the keys an APEX v1 project uses.
- [`ape config pin --help`](../reference/cli.md#ape-config-pin).
