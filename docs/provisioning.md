# What is provisioning?

**Provisioning** is setting up the empty workshop before anyone starts working: making
sure the notebook, the message tray, the supply cupboard, and the phone book by the
way in exist, and that they're the *right kind*.

You run it once, and you can safely run it again any time — on every restart, in a
setup script, whenever. It follows one simple rule:

> **If something is missing, make it. If it already exists, look at it — don't
> rearrange it.**

So:

- **Empty workshop?** Provisioning creates the notebook, the cupboard, and the phone
  book ([the persona directory](./persona-directory.md)), set up exactly the way
  Soulstream needs. It tells you "created, created, created".
- **Already set up correctly?** Provisioning changes nothing and tells you
  "conformant, conformant, conformant". Running it ten times in a row does the same
  thing as running it once.
- **Half set up?** (Say the cupboard is there but the notebook isn't.) Provisioning
  makes only the missing part and leaves the rest alone.

## Why won't it "fix" a notebook that's set up wrong?

Say someone earlier made a notebook that **throws away old pages after a month**.
That's not how a Soulstream notebook should work — but if provisioning quietly changed
that setting, it could **delete history that's already there**. That's a step you can't
un-open.

So provisioning refuses to touch it. Instead it tells you plainly:

> `stream nonconformant [MaxAge is set to 720h0m0s (age-based expiry present)]`

Now *you* decide what to do — because you're the only one who knows whether that
history matters. Looking is safe; silently rearranging someone's notebook is not.

## The one planned renovation

There is a single exception, and it's deliberate. Workshops set up before the message
tray existed had one notebook that collected *everything* — topic history, shoulder
taps, even the shouted questions that were never meant to be kept. Provisioning
recognises exactly that old layout and renovates it, once:

1. the notebook goes back to holding topic history only (every other setting on it —
   even a size limit an operator added — is left exactly as found);
2. the message tray is put by the door;
3. each persona's newest 100 shoulder-tap notes are moved from the notebook into the
   tray, unchanged (their wax seals still check out);
4. the notes and stale shouts left behind in the notebook are cleared away.

It reports `updated` for that run, and `conformant` ever after. Any layout it does
*not* recognise is still only reported, never touched.

## Storage budgets — how big may each shelf be?

Think of the realm's four fixtures as shelves in a rented workshop. In your own
garage a shelf can grow as tall as it likes. But some landlords (hosted NATS
accounts like NGS's free tier) have a house rule: **no shelf goes up without a
size written on the order form.** Ask for a shelf with no size and the landlord
simply refuses — that's the error provisioning used to hit on such accounts,
and the operator had to build every shelf by hand first.

Budgets fix that. Tell provisioning how big each shelf may get, or just say
"use the proven sizes":

```sh
soulstream provision --budgets            # the proven sizes, all four shelves
soulstream provision --budgets --budget-objects 2GiB   # same, bigger file shelf
soulstream provision --budget-oplog 10GiB # only the history shelf gets a size
```

The proven sizes (they fit the known strict landlord, nothing more): history
notebook 1 GiB, message tray 64 MiB, phone book 64 MiB, supply cupboard
512 MiB. Sizes are written the binary way — KiB, MiB, GiB.

Three things budgets never do:

- **They never resize an existing shelf.** Provisioning stays look-don't-touch:
  a shelf that's already standing keeps its size, and the report simply shows
  it (`unlimited` when it has none).
- **They never sneak in.** No flags → no budgets → exactly the old behavior,
  including the same clear refusal from a strict landlord.
- **They never make the message tray bottomless.** The tray is bounded by
  design; leaving its budget out keeps its standard 64 MiB roof.

## Related

- [The realm](./realm.md) — the workshop that gets provisioned.
- [The persona directory](./persona-directory.md) — the phone book, the third fixture.
