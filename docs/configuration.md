# Configuration: the sticker on the folder

How do the Soulstream tools know **who you are and where you're working**? Think of a
**name sticker on a project folder**. Walk up to any desk carrying that folder, and
everyone knows: *this* work happens in *that* realm, as *that* persona. A different
folder can wear a different sticker — same person, different hat.

## The five things the tools need

Every tool — the `soulstream` remote control and the MCP door agents use — needs up to
five answers: which saved **NATS context** to dial, which **realm**, which
**persona**, and (optionally) where the **signing key** and the **pin notebook** live.

## Four places an answer can come from

Each of the five is answered separately, and the first place that has an answer wins:

1. **A flag** you typed right now (`--realm acme`) — you said it, it wins.
2. **An environment variable** (`SOULSTREAM_REALM=acme`) — set for this shell.
3. **The project's sticker** — a `.soulstream.json` file in the project directory
   (or any directory above it — the *nearest* one is the sticker; they don't stack).
4. **Your home sticker** — `config.json` in your soulstream config directory, right
   beside your keys. Machine-wide defaults live here (usually just your context).

Nothing anywhere? The field stays empty, and things behave as before — reading works,
writing asks for a persona.

A sticker looks like this (every line optional):

```json
{ "realm": "acme", "persona": "ada" }
```

So the common setup is: your **context** once, in the home sticker; **realm and
persona** per project, in each folder's sticker. Change directory, change identity —
no flags, no environment juggling. The MCP door works the same way: it looks at the
directory it was started in, which is exactly the project your assistant has open.

## "Where did THAT come from?"

When something connects to the wrong realm, don't guess:

```sh
soulstream config
```

prints all five answers **and the place each one came from** — this flag, that
variable, this file (with its full path), or "unset".

## The sticker names you — it can never BE you

A sticker is just names and paths. It cannot hold a key, a password, or a seal. Your
**signing key stays in your own drawer** (the keystore), filed under realm + persona.
If someone hands you a folder whose sticker says "sign as the queen", nothing happens:
you don't have the queen's seal in your drawer, so your notes simply go out unsealed.
Naming an identity grants nothing.

Two small print rules: a sticker with a spelling mistake (a field the tools don't
know, or broken JSON) stops everything with a clear message naming the file — a typo
must never quietly send you to the wrong realm. And if a sticker points at a key file
with a relative path (`./keys/ci.ed25519`), that path counts from the sticker's own
folder, wherever you happen to be standing.

## The lane an agent arrives on

A saved NATS context assumes somebody sat at this machine and saved one. An agent
handed a credential by whoever created it has nothing saved anywhere, so the MCP
door takes its whole connection from three answers that never touch a sticker:

| Flag | Variable | What it is |
|---|---|---|
| `--url` | `SOULSTREAM_URL` | the server address to dial |
| `--creds` | `SOULSTREAM_CREDS` | a credentials file — on this lane, the deployment's public sentinel |
| `--token` | `SOULSTREAM_TOKEN` | the access token that says which agent this is |

Prefer the variables. A token on a command line is visible to every process on the
machine; a variable set by whatever launched the door is not.

The sentinel plus the token is the **revocable lane**: neither half admits anybody
alone, the realm exchanges the pair for a scoped identity that expires on its own,
and taking the token away refuses the next connection. That is why these three are
flags and variables and never sticker fields — the rule above holds, a config file
can name you but never be you.

An address given here wins over a saved context, and says so on the way past, so
nobody has to wonder which server they reached.
