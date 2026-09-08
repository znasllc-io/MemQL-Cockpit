# `memql access` — what this cluster says you are

One command, one question: **what am I on this cluster, and why was I just
refused?**

```
$ memql access acme
Your access on "acme"

  Signed in as    Ada Lovelace <ada@example.com>
  User id         u_01JQ8ZK
  Session         sess_01JQ90F  (this device)

  Role            Release Manager
                  release-manager · rank 150
  Groups          Platform (team) — Acme Corp
                  Release Captains (role-group) — Acme Corp
  Account scope   acct_9
```

The cockpit half of the access program's roles-as-data epic
(memql-cockpit#403; engine epic memql#5166). The design record lives in the
**engine** repository:
`docs/superpowers/specs/2026-09-07-roles-as-data-design.md`, section G, with
the program index beside it at `2026-09-07-access-program.md`. This repository
has no separate record.

---

## Why a command and not a portal page

The portal renders what the cluster believes about a **browser session**. A
person hitting a refusal *from this machine* needs what the cluster believes
about **this credential** — and those differ exactly when it matters: a stale
token, a PAT with a different ceiling, a second account, a machine somebody
else signed in on months ago.

`memql access` asks the cluster over the credential it would actually use.

**It asks rather than decoding the bearer.** The cockpit does not parse its own
tokens — the standing client rule that `session_id` and `display_name` exist on
the wire to serve. A claim is what was true when the token was minted; a role
changed since is the whole thing somebody running this command is trying to
find out.

---

## The role is a slug, a name and a rank

Not one of five enum values. A **custom role is none of the five**, and its
**rank** is what says where it stands in the ladder — so the cockpit prints all
three and compares the role to nothing.

```
  Role            Release Manager           <- role_name
                  release-manager · rank 150
                  ^ role (the catalog slug)   ^ rank
```

The slug is repeated under the name because the slug is what every grant,
invitation and delegation ceiling is written in terms of; the name is only what
it is called.

### Rank 0 gets a sentence of its own

```
  Role            retired-role
                  rank 0 — this role holds no permissions on this cluster
```

Rank 0 is the record's **unknown slug**: the resolver treats the holder as
holding nothing, everywhere, until they are re-roled. It is the documented
consequence of the cluster owner's direct-write escape (a role row deactivated
under a holder). It is also the single fact that explains every refusal the
reader is about to hit, so a bare `rank 0` would bury it.

---

## Today: the cluster does not report it yet

```
  Role            not reported by this cluster
  Groups          not reported by this cluster
  Account scope   not reported by this cluster

This cluster does not report roles as data yet. The engine change that adds
the role slug, its name and its rank to MyAccess is memql#5181; until that
lands there is nothing here to show. The cockpit does NOT fall back to the
retired five-value enum: a custom role is none of those five, so a guess
would be wrong in exactly the case this command exists for.
```

**This is the expected state at the current pin**, and it is not a failure.
`MyAccessResult` still carries the old enum; the fields this reads
(`role`, `role_name`, `rank` from memql#5181, and `groups`, `account_ids`,
`every_account` from memql#5165) do not exist on the wire yet.

**Nothing needs to change here when they land.** The cockpit reads them off the
message descriptor by name, so the fields simply start existing and the command
starts printing them — no cockpit release, no pin-bump code change.

### An absence is a sentence, never a blank

Every line here has a state where the cluster said nothing, and a blank or a
dash reads as the **opposite** claim — "you hold no role" instead of "this
cluster did not say". The first sends somebody to ask for a grant they already
have. So the two never render alike. It is the rule `probe.Figure` keeps for a
measurement that could not be taken.

---

## By name, never by number

Both design records that add fields to `MyAccessResult` settle the **names**
and hand the **numbers** to whoever writes the engine change:

> Field numbers are chosen by the implementer against the current message; the
> names are the contract.
> — *groups-and-grants*, on the wire

> gains `string role = 11`, `string role_name = 12`, `int32 rank = 13` (numbers
> chosen against the message by the implementer …)
> — *roles-as-data*, section G

So a number written into the cockpit is a **guess the engine is free to
contradict**, and it would fail in the worst way: whatever the landed message
puts at field 11 would render as the operator's role.

It also rules out the obvious alternative. Proto keeps unrecognised fields as
`unknownFields` **bytes keyed by number only** — the name never travels on the
wire — so there is no way to pull `role` out of an unknown-field blob without
already knowing the number the records decline to settle. Reading the
**descriptor by name** is the only correct option, and it is the one that needs
no cockpit change when the field lands.

`internal/access` also checks the **kind**, so a future field reusing one of
these names for a different type reads as absent rather than being coerced.
`FuzzDecodeMatchesFieldNamesExactly` is the guard: a field populates a slot only
when its name matches character for character and its kind and cardinality
match too. It catches the helpful-looking change — matching case-insensitively,
trimming, accepting `clusterRole` as a synonym — that would render a value the
cluster never claimed.

---

## Choosing the cluster

```
memql access                  # the selected cluster, or the only one
memql access acme             # by name
memql access --json           # the same report, for scripts
```

With no argument the command uses `selected_cluster` from
`~/.memql/clusters.yaml` (the same value the VS Code extension reads), or the
only registered cluster when there is exactly one.

**Several registered and none selected is an error, not a guess.** Reporting
somebody's access on a cluster they did not name is worse than asking, because
the answer looks exactly like the one they wanted — and being believed is the
entire job of this command.

---

## `--json`

```json
{
  "cluster": "acme",
  "user_id": "u_01JQ8ZK",
  "role": { "reported": true, "slug": "release-manager", "name": "Release Manager", "rank": 150 },
  "groups": { "reported": true, "items": [ … ] },
  "account_scope": { "reported": true, "account_ids": ["acct_9"], "every_account": false }
}
```

Every block that can be absent is an **object with an explicit `reported`**, and
its contents are omitted entirely when it is false. JSON makes the confusion
easier than prose does — a missing key and a `false` one are both falsey to
every caller that reads them — and a script asking "may this machine deploy?"
must not read a cluster that never mentioned roles as a person who holds none.

`rank` is emitted whenever it was reported, **including 0**, because `omitempty`
on a plain integer would delete precisely the value a caller most needs to see.

---

## The credential

The **signed-in user's**, resolved interactively: a person typed this command,
so a browser sign-in is the right answer to an expired token rather than a
window nobody will open. (The worker's own paths do the opposite, deliberately —
see `EnsureValidTokenNonInteractive`.)

A `mql_wkr_` worker token cannot be used here: it is admitted on WorkerService
and nowhere else. A PAT works, and shows an empty session — a PAT names no
session row, which the wire calls out as not an error:

```
  Session         none — this credential carries no session
```

---

## A refusal arrives as a successful response

`MyAccess` answers with a `QueryError` inside a perfectly good message rather
than a transport error, so a client that only checked the error return would
render an empty record — which is the same shape as the honest answer for
somebody who really does hold nothing. Every refusal is read and printed
verbatim, naming the cluster.
